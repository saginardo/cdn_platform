package control

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/nginx"
	"cdn-platform/internal/store"
)

type Publisher struct {
	Store  *store.Store
	Cipher *Cipher
}

func (p Publisher) PublishSite(siteID string) (domain.DeploymentTask, error) {
	site, _, err := p.Store.GetSite(siteID)
	if err != nil {
		return domain.DeploymentTask{}, err
	}
	if site.Deleting {
		return domain.DeploymentTask{}, store.ErrSiteDeleting
	}
	deadline := time.Now().UTC().Add(90 * time.Second)
	task, created, err := p.Store.CreateOrGetActivePublishTask(siteID, deadline)
	if err != nil {
		return domain.DeploymentTask{}, err
	}
	if !created {
		return task, nil
	}
	if err := p.Store.UpdateTask(task.ID, domain.TaskDispatching, "building node configurations"); err != nil {
		return task, err
	}
	if err := p.Store.UpdateTask(task.ID, domain.TaskApplying, "preparing edge configuration confirmation"); err != nil {
		return task, err
	}
	targets, err := p.publishSite(siteID, task.ID)
	if err != nil {
		_ = p.Store.UpdateTask(task.ID, domain.TaskFailed, err.Error())
		return task, err
	}
	if _, err := p.Store.MarkSitePublished(siteID); err != nil {
		_ = p.Store.UpdateTask(task.ID, domain.TaskFailed, err.Error())
		return task, err
	}
	if len(targets) == 0 {
		_ = p.Store.UpdateTask(task.ID, domain.TaskSucceeded, "configuration staged; no active assigned edge nodes to confirm")
	}
	return p.Store.GetTask(task.ID)
}

func (p Publisher) PublishAll() error {
	sites, err := p.Store.ListSites()
	if err != nil {
		return err
	}
	for _, site := range sites {
		if !site.Published || site.Deleting {
			continue
		}
		if _, err := p.PublishSite(site.ID); err != nil {
			return err
		}
	}
	return nil
}

func (p Publisher) publishSite(siteID, publishTaskID string) ([]store.PublishTaskNode, error) {
	updates, targets, err := p.prepareNodeStates(siteID, false)
	if err != nil {
		return nil, err
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("no eligible edge nodes are available for publication")
	}
	// Persist targets before exposing the desired state. An agent can poll and
	// fail quickly (for example due to a port conflict), so a later target
	// insert would lose its first structured report.
	if err := p.Store.CreatePublishTaskNodes(publishTaskID, targets); err != nil {
		return nil, err
	}
	if err := p.Store.SaveNodeStates(updates); err != nil {
		return nil, err
	}
	return targets, nil
}

func (p Publisher) PrepareSiteRemoval(siteID string) ([]store.NodeStateUpdate, []store.PublishTaskNode, error) {
	return p.prepareNodeStates(siteID, true)
}

func (p Publisher) prepareNodeStates(siteID string, removing bool) ([]store.NodeStateUpdate, []store.PublishTaskNode, error) {
	targetSite, _, err := p.Store.GetSite(siteID)
	if err != nil {
		return nil, nil, err
	}
	if !removing && targetSite.Deleting {
		return nil, nil, store.ErrSiteDeleting
	}
	allSites, err := p.Store.ListSites()
	if err != nil {
		return nil, nil, err
	}
	nodes, err := p.Store.ListNodes()
	if err != nil {
		return nil, nil, err
	}
	updates := make([]store.NodeStateUpdate, 0, len(nodes))
	targets := make([]store.PublishTaskNode, 0)
	for _, node := range nodes {
		if node.Status == domain.NodeRevoked || node.Status == domain.NodeUninstalling || node.Status == domain.NodeUninstalled {
			continue
		}
		nodeSites := assignedPublishedSites(allSites, node.ID, siteID)
		if removing {
			nodeSites = assignedPublishedSitesExcluding(allSites, node.ID, siteID)
		}
		for _, assignedSite := range nodeSites {
			certificateCiphertext, keyCiphertext, _, certificateErr := p.Store.Certificate(assignedSite.ID)
			if certificateErr != nil {
				return nil, nil, fmt.Errorf("site %s needs a certificate before it can be published to node %s", assignedSite.Name, node.Name)
			}
			certificatePEM, err := p.Cipher.Decrypt(certificateCiphertext)
			if err != nil {
				return nil, nil, fmt.Errorf("decrypt certificate for %s: %w", assignedSite.Name, err)
			}
			if err := validateCertificateDomains(certificatePEM, assignedSite.Domains, time.Now().UTC()); err != nil {
				return nil, nil, fmt.Errorf("site %s certificate: %w", assignedSite.Name, err)
			}
			privateKeyPEM, err := p.Cipher.Decrypt(keyCiphertext)
			if err != nil {
				return nil, nil, fmt.Errorf("decrypt private key for %s: %w", assignedSite.Name, err)
			}
			if err := validateCertificatePrivateKey(certificatePEM, privateKeyPEM); err != nil {
				return nil, nil, fmt.Errorf("site %s certificate private key: %w", assignedSite.Name, err)
			}
		}
		config, err := nginx.Render(nodeSites)
		if err != nil {
			return nil, nil, err
		}
		version := int64(1)
		if previous, _, stateErr := p.Store.NodeState(node.ID); stateErr == nil {
			version = previous.Version + 1
		} else if stateErr != nil && stateErr != store.ErrNotFound {
			return nil, nil, stateErr
		}
		state := domain.DesiredState{Version: version, NginxConfig: config, PublicPorts: requiredPublicPorts(nodeSites), Certificates: make(map[string]domain.TLSBundle)}
		for _, assignedSite := range nodeSites {
			cert, key, _, certificateErr := p.Store.Certificate(assignedSite.ID)
			if certificateErr != nil {
				continue
			}
			certificatePEM, err := p.Cipher.Decrypt(cert)
			if err != nil {
				return nil, nil, fmt.Errorf("decrypt certificate for %s: %w", assignedSite.Name, err)
			}
			privateKeyPEM, err := p.Cipher.Decrypt(key)
			if err != nil {
				return nil, nil, fmt.Errorf("decrypt private key for %s: %w", assignedSite.Name, err)
			}
			state.Certificates[assignedSite.ID] = domain.TLSBundle{CertificatePEM: string(certificatePEM), PrivateKeyPEM: string(privateKeyPEM)}
		}
		serialized, err := json.Marshal(state.Certificates)
		if err != nil {
			return nil, nil, err
		}
		encryptedCertificates, err := p.Cipher.Encrypt(serialized)
		if err != nil {
			return nil, nil, err
		}
		updates = append(updates, store.NodeStateUpdate{NodeID: node.ID, State: state, CertificatesCiphertext: encryptedCertificates})
		isTarget := isAssignedToSite(siteID, nodeSites)
		if removing {
			isTarget = siteHasNode(targetSite, node.ID)
		}
		if node.Status == domain.NodeActive && isTarget {
			targets = append(targets, store.PublishTaskNode{NodeID: node.ID, TargetVersion: version})
		}
	}
	return updates, targets, nil
}

func isAssignedToSite(siteID string, sites []domain.Site) bool {
	for _, site := range sites {
		if site.ID == siteID {
			return true
		}
	}
	return false
}

func requiredPublicPorts(sites []domain.Site) []int {
	if len(sites) == 0 {
		return []int{80}
	}
	return []int{80, 443}
}

func validateCertificateDomains(certificatePEM []byte, domains []string, now time.Time) error {
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("invalid PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if !certificate.NotAfter.After(now) {
		return fmt.Errorf("expired at %s", certificate.NotAfter.UTC().Format(time.RFC3339))
	}
	for _, domainName := range domains {
		if err := certificate.VerifyHostname(domainName); err != nil {
			return fmt.Errorf("does not cover %s", domainName)
		}
	}
	return nil
}

func validateCertificatePrivateKey(certificatePEM, privateKeyPEM []byte) error {
	_, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	return err
}

func assignedSites(sites []domain.Site, nodeID string) []domain.Site {
	var result []domain.Site
	for _, site := range sites {
		for _, assigned := range site.Nodes {
			if assigned == nodeID {
				result = append(result, site)
				break
			}
		}
	}
	return result
}

func assignedPublishedSites(sites []domain.Site, nodeID, publishingSiteID string) []domain.Site {
	var result []domain.Site
	for _, site := range sites {
		if !site.Enabled || site.Deleting {
			continue
		}
		if !site.Published && site.ID != publishingSiteID {
			continue
		}
		for _, assigned := range site.Nodes {
			if assigned == nodeID {
				result = append(result, site)
				break
			}
		}
	}
	return result
}

func assignedPublishedSitesExcluding(sites []domain.Site, nodeID, excludedSiteID string) []domain.Site {
	var result []domain.Site
	for _, site := range sites {
		if site.ID == excludedSiteID || site.Deleting || !site.Enabled || !site.Published {
			continue
		}
		if siteHasNode(site, nodeID) {
			result = append(result, site)
		}
	}
	return result
}

func (p Publisher) StoreCertificate(siteID string, certificatePEM, privateKeyPEM []byte, notAfter time.Time) error {
	if err := validateCertificatePrivateKey(certificatePEM, privateKeyPEM); err != nil {
		return fmt.Errorf("validate certificate private key: %w", err)
	}
	certificate, err := p.Cipher.Encrypt(certificatePEM)
	if err != nil {
		return err
	}
	key, err := p.Cipher.Encrypt(privateKeyPEM)
	if err != nil {
		return err
	}
	return p.Store.SaveCertificate(siteID, certificate, key, &notAfter)
}
