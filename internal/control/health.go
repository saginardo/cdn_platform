package control

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/nginx"
)

type HealthManager struct {
	Server          *Server
	Client          *http.Client
	SiteProbe       func(context.Context, domain.Site, domain.Node) (bool, string)
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
	if m.Server.SiteDeleter != nil {
		if err := m.Server.SiteDeleter.Reconcile(ctx); err != nil {
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
		healthy, detail := m.checkNode(ctx, node)
		health, err := m.Server.Store.RecordNodeHealth(node.ID, healthy, detail)
		if err != nil {
			return err
		}
		if prior.DNSEligible && !health.DNSEligible && m.Server.Notifier != nil {
			_ = m.Server.Notifier.Notify(ctx, "CDN alert: edge node removed from DNS pool", "Node "+node.Name+" ("+node.PublicIPv4+") failed three consecutive health checks: "+detail)
		}
	}
	drafts, err := m.Server.Store.ListSites()
	if err != nil {
		return err
	}
	publications, err := m.Server.Store.ListSitePublications()
	if err != nil {
		return err
	}
	draftsByID := make(map[string]domain.Site, len(drafts))
	publishedByID := make(map[string]bool, len(publications))
	for _, publication := range publications {
		publishedByID[publication.Site.ID] = true
	}
	for _, draft := range drafts {
		draftsByID[draft.ID] = draft
		if draft.Deleting || (!draft.Enabled && !publishedByID[draft.ID]) {
			if err := m.clearSiteDNS(ctx, draft); err != nil {
				return err
			}
			m.clearNoHealthyAlert(draft.ID)
		}
	}
	for _, publication := range publications {
		site := publication.Site
		if draft, found := draftsByID[site.ID]; !found || draft.Deleting {
			continue
		}
		if !site.Enabled {
			if err := m.clearSiteDNS(ctx, site); err != nil {
				return err
			}
			m.clearNoHealthyAlert(site.ID)
			continue
		}
		if err := m.reconcileSite(ctx, site, nodes); err != nil {
			return err
		}
	}
	return nil
}

func (m *HealthManager) clearSiteDNS(ctx context.Context, site domain.Site) error {
	if err := m.Server.DNS.Reconcile(ctx, site.ZoneID, "site="+site.ID, nil); err != nil {
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
		nodeHealth, err := m.Server.Store.NodeHealth(node.ID)
		if err != nil {
			return err
		}
		if !nodeHealth.DNSEligible {
			continue
		}
		state, _, err := m.Server.Store.NodeState(node.ID)
		if err != nil {
			return err
		}
		hasSiteHealth := nginx.HasSiteHealth(state.NginxConfig, site.ID)
		if nodeHealth.LastError != "" {
			// Preserve both hysteresis decisions, but do not multiply a current
			// reachability failure into one HTTPS timeout per hosted site.
			if !hasSiteHealth {
				healthy = append(healthy, node)
				continue
			}
			siteHealth, err := m.Server.Store.SiteNodeHealth(site.ID, node.ID)
			if err != nil {
				return err
			}
			if siteHealth.DNSEligible {
				healthy = append(healthy, node)
			}
			continue
		}
		if hasSiteHealth {
			prior, err := m.Server.Store.SiteNodeHealth(site.ID, node.ID)
			if err != nil {
				return err
			}
			probeHealthy, detail := m.siteCheck(ctx, site, node)
			siteHealth, err := m.Server.Store.RecordSiteNodeHealth(site.ID, node.ID, probeHealthy, detail)
			if err != nil {
				return err
			}
			if prior.DNSEligible && !siteHealth.DNSEligible && m.Server.Notifier != nil {
				_ = m.Server.Notifier.Notify(ctx, "CDN alert: site endpoint removed from DNS pool", "Site "+site.Name+" on node "+node.Name+" ("+node.PublicIPv4+") failed three consecutive HTTPS/SNI health checks: "+siteHealth.LastError)
			}
			if !siteHealth.DNSEligible {
				continue
			}
		}
		healthy = append(healthy, node)
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
	ttl := domain.DefaultDNSTTLSeconds
	if m.Server.Settings != nil {
		ttl = m.Server.Settings.DNSTTL(site)
	} else if site.DNSTTLSeconds != nil {
		ttl = *site.DNSTTLSeconds
	}
	for _, node := range healthy {
		for _, domainName := range site.Domains {
			desired = append(desired, integrations.DNSRecord{
				Name: domainName, Content: node.PublicIPv4, TTL: ttl, Proxied: false,
				Comment: integrations.ManagedRecordPrefix + "site=" + site.ID + ";node=" + node.ID,
			})
		}
	}
	if err := m.Server.DNS.Reconcile(ctx, site.ZoneID, "site="+site.ID, desired); err != nil {
		return fmt.Errorf("reconcile DNS for %s: %w", site.Name, err)
	}
	return nil
}

func (m *HealthManager) checkNode(ctx context.Context, node domain.Node) (bool, string) {
	state, _, err := m.Server.Store.NodeState(node.ID)
	if err != nil || state.PublicPorts == nil {
		return m.check(ctx, node.PublicIPv4)
	}
	for _, port := range state.PublicPorts {
		if port == 80 {
			return m.check(ctx, node.PublicIPv4)
		}
	}
	if len(state.PublicPorts) == 0 {
		return true, ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dialer := net.Dialer{Timeout: 3 * time.Second, KeepAlive: -1}
	for _, port := range state.PublicPorts {
		connection, err := dialer.DialContext(probeCtx, "tcp", net.JoinHostPort(node.PublicIPv4, fmt.Sprintf("%d", port)))
		if err != nil {
			return false, fmt.Sprintf("TCP %d: %v", port, err)
		}
		_ = connection.Close()
	}
	return true, ""
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

func (m *HealthManager) siteCheck(ctx context.Context, site domain.Site, node domain.Node) (bool, string) {
	if m.SiteProbe != nil {
		return m.SiteProbe(ctx, site, node)
	}
	return m.checkSite(ctx, site, node)
}

func (m *HealthManager) checkSite(ctx context.Context, site domain.Site, node domain.Node) (bool, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(node.PublicIPv4, "443"))
		},
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DisableKeepAlives:     true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	want := nginx.SiteHealthBody(site.ID)
	for _, domainName := range site.Domains {
		endpoint := (&url.URL{Scheme: "https", Host: domainName, Path: "/__cdn_health"}).String()
		request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			return false, domainName + ": " + err.Error()
		}
		response, err := client.Do(request)
		if err != nil {
			return false, domainName + ": " + err.Error()
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 513))
		closeErr := response.Body.Close()
		if readErr != nil {
			return false, domainName + ": read health response: " + readErr.Error()
		}
		if closeErr != nil {
			return false, domainName + ": close health response: " + closeErr.Error()
		}
		if response.StatusCode != http.StatusOK {
			return false, domainName + ": health endpoint returned " + response.Status
		}
		if strings.TrimSpace(string(body)) != want {
			return false, fmt.Sprintf("%s: unexpected health response %q", domainName, strings.TrimSpace(string(body)))
		}
	}
	return checkTCPForwardPorts(probeCtx, site, node)
}

func checkTCPForwardPorts(ctx context.Context, site domain.Site, node domain.Node) (bool, string) {
	dialer := net.Dialer{Timeout: 3 * time.Second, KeepAlive: -1}
	for _, forward := range site.TCPForwards {
		connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(node.PublicIPv4, fmt.Sprintf("%d", forward.ListenPort)))
		if err != nil {
			return false, fmt.Sprintf("TCP %d: %v", forward.ListenPort, err)
		}
		_ = connection.Close()
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
