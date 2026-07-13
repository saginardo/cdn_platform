package control

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
)

type HealthManager struct {
	Server          *Server
	Client          *http.Client
	alertMu         sync.Mutex
	noHealthyAlerts map[string]bool
}

func (m *HealthManager) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if err := m.Reconcile(ctx); err != nil && m.Server != nil && m.Server.Logger != nil {
			m.Server.Logger.Error("health reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *HealthManager) Reconcile(ctx context.Context) error {
	if m.Server == nil {
		return nil
	}
	if m.Server.Store != nil {
		if err := m.Server.Store.ReconcilePublishTasks(); err != nil {
			return err
		}
	}
	if m.Server.DNS == nil {
		return nil
	}
	nodes, err := m.Server.Store.ListNodes()
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Status == domain.NodeRevoked || node.Status == domain.NodeDraining || node.Status == domain.NodeUninstalling || node.Status == domain.NodeUninstalled {
			continue
		}
		prior, err := m.Server.Store.NodeHealth(node.ID)
		if err != nil {
			return err
		}
		healthy, detail := m.check(ctx, node.PublicIPv4)
		health, err := m.Server.Store.RecordNodeHealth(node.ID, healthy, detail)
		if err != nil {
			return err
		}
		if prior.DNSEligible && !health.DNSEligible && m.Server.Notifier != nil {
			_ = m.Server.Notifier.Notify(ctx, "CDN alert: edge node removed from DNS pool", "Node "+node.Name+" ("+node.PublicIPv4+") failed three consecutive health checks: "+detail)
		}
	}
	sites, err := m.Server.Store.ListSites()
	if err != nil {
		return err
	}
	for _, site := range sites {
		if !site.Enabled {
			if err := m.clearSiteDNS(ctx, site); err != nil {
				return err
			}
			m.clearNoHealthyAlert(site.ID)
			continue
		}
		if !site.Published {
			continue
		}
		if err := m.reconcileSite(ctx, site, nodes); err != nil {
			return err
		}
	}
	return nil
}

func (m *HealthManager) clearSiteDNS(ctx context.Context, site domain.Site) error {
	_, zoneID, err := m.Server.Store.GetSite(site.ID)
	if err != nil {
		return err
	}
	if err := m.Server.DNS.Reconcile(ctx, zoneID, "site="+site.ID, nil); err != nil {
		return fmt.Errorf("remove DNS for disabled site %s: %w", site.Name, err)
	}
	return nil
}

func (m *HealthManager) reconcileSite(ctx context.Context, site domain.Site, nodes []domain.Node) error {
	nodesByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		nodesByID[node.ID] = node
	}
	var healthy []domain.Node
	activeAssigned := 0
	for _, nodeID := range site.Nodes {
		node, found := nodesByID[nodeID]
		if !found || node.Status != domain.NodeActive {
			continue
		}
		activeAssigned++
		desiredVersion, err := m.Server.Store.DesiredVersion(node.ID)
		if err != nil {
			return err
		}
		if desiredVersion == 0 || node.AppliedVersion < desiredVersion {
			continue
		}
		health, err := m.Server.Store.NodeHealth(node.ID)
		if err != nil {
			return err
		}
		if health.DNSEligible {
			healthy = append(healthy, node)
		}
	}
	if len(healthy) == 0 {
		if activeAssigned == 0 {
			if err := m.clearSiteDNS(ctx, site); err != nil {
				return err
			}
			m.clearNoHealthyAlert(site.ID)
			return nil
		}
		if m.markNoHealthyAlert(site.ID) && m.Server.Notifier != nil {
			_ = m.Server.Notifier.Notify(ctx, "CDN alert: no healthy nodes for "+site.Name, "DNS was left unchanged because every assigned node is unhealthy. Investigate edge reachability and control-plane probes.")
		}
		return nil
	}
	m.clearNoHealthyAlert(site.ID)
	desired := make([]integrations.DNSRecord, 0, len(healthy)*len(site.Domains))
	for _, node := range healthy {
		for _, domainName := range site.Domains {
			desired = append(desired, integrations.DNSRecord{
				Name: domainName, Content: node.PublicIPv4, TTL: 60, Proxied: false,
				Comment: integrations.ManagedRecordPrefix + "site=" + site.ID + ";node=" + node.ID,
			})
		}
	}
	_, zoneID, err := m.Server.Store.GetSite(site.ID)
	if err != nil {
		return err
	}
	if err := m.Server.DNS.Reconcile(ctx, zoneID, "site="+site.ID, desired); err != nil {
		return fmt.Errorf("reconcile DNS for %s: %w", site.Name, err)
	}
	return nil
}

func (m *HealthManager) check(ctx context.Context, address string) (bool, string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/__cdn_health", nil)
	if err != nil {
		return false, err.Error()
	}
	response, err := m.client().Do(request)
	if err != nil {
		return false, err.Error()
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, "health endpoint returned " + response.Status
	}
	return true, ""
}

func (m *HealthManager) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (m *HealthManager) markNoHealthyAlert(siteID string) bool {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	if m.noHealthyAlerts == nil {
		m.noHealthyAlerts = make(map[string]bool)
	}
	if m.noHealthyAlerts[siteID] {
		return false
	}
	m.noHealthyAlerts[siteID] = true
	return true
}

func (m *HealthManager) clearNoHealthyAlert(siteID string) {
	m.alertMu.Lock()
	defer m.alertMu.Unlock()
	delete(m.noHealthyAlerts, siteID)
}

type MemoryDNS struct {
	mu    sync.Mutex
	Zones map[string][]integrations.DNSRecord
}

func (m *MemoryDNS) Reconcile(_ context.Context, zoneID, _ string, desired []integrations.DNSRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Zones == nil {
		m.Zones = make(map[string][]integrations.DNSRecord)
	}
	m.Zones[zoneID] = append([]integrations.DNSRecord(nil), desired...)
	return nil
}

func (m *MemoryDNS) RemoveNode(_ context.Context, zoneID, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	records := m.Zones[zoneID]
	kept := records[:0]
	for _, record := range records {
		if memoryDNSRecordMatchesNode(record.Comment, nodeID) {
			continue
		}
		kept = append(kept, record)
	}
	m.Zones[zoneID] = append([]integrations.DNSRecord(nil), kept...)
	return nil
}

func memoryDNSRecordMatchesNode(comment, nodeID string) bool {
	if !strings.HasPrefix(comment, integrations.ManagedRecordPrefix) {
		return false
	}
	for _, field := range strings.Split(strings.TrimPrefix(comment, integrations.ManagedRecordPrefix), ";") {
		key, value, found := strings.Cut(field, "=")
		if found && key == "node" && value == nodeID {
			return true
		}
	}
	return false
}
