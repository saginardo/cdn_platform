package control

import (
	"context"
	"crypto/tls"
	"errors"
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
	WorkerLimit     int
	RoundTimeout    time.Duration
	Interval        time.Duration
	alertMu         sync.Mutex
	noHealthyAlerts map[string]bool
	statusMu        sync.RWMutex
	lastRound       HealthRoundStatus
}

type HealthRoundStatus struct {
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	ErrorCount int       `json:"error_count"`
	TimedOut   bool      `json:"timed_out"`
	Error      string    `json:"error,omitempty"`
}

const (
	defaultHealthWorkerLimit  = 4
	defaultHealthRoundTimeout = 45 * time.Second
	defaultHealthInterval     = 15 * time.Second
)

func (m *HealthManager) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		roundStarted := time.Now()
		_ = m.Reconcile(ctx)
		delay := m.interval() - time.Since(roundStarted)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (m *HealthManager) Reconcile(ctx context.Context) error {
	started := time.Now().UTC()
	roundCtx, cancel := context.WithTimeout(ctx, m.roundTimeout())
	defer cancel()
	errorsFound := make([]error, 0)
	finish := func() error {
		joined := errors.Join(errorsFound...)
		m.recordRound(started, len(errorsFound), errors.Is(roundCtx.Err(), context.DeadlineExceeded), joined)
		return joined
	}
	if m.Server == nil {
		return finish()
	}
	if m.Server.Store != nil {
		if err := m.Server.Store.ReconcilePublishTasks(); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("reconcile publish tasks: %w", err))
		}
		if err := m.Server.Store.ReconcileNodeUpgrades(); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("reconcile node upgrades: %w", err))
		}
		if err := m.Server.Store.ReconcileSecurity(); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("reconcile security state: %w", err))
		}
	}
	if m.Server.SiteDeleter != nil {
		if err := m.Server.SiteDeleter.Reconcile(roundCtx); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("reconcile site deletions: %w", err))
		}
	}
	if m.Server.DNS == nil {
		return finish()
	}
	nodes, err := m.Server.Store.ListNodes()
	if err != nil {
		errorsFound = append(errorsFound, fmt.Errorf("list nodes: %w", err))
		return finish()
	}
	errorsFound = append(errorsFound, m.reconcileNodeHealth(roundCtx, nodes)...)
	if roundCtx.Err() != nil {
		errorsFound = append(errorsFound, m.roundContextError(roundCtx))
		return finish()
	}
	drafts, err := m.Server.Store.ListSites()
	if err != nil {
		errorsFound = append(errorsFound, fmt.Errorf("list site drafts: %w", err))
		return finish()
	}
	publications, err := m.Server.Store.ListSitePublications()
	if err != nil {
		errorsFound = append(errorsFound, fmt.Errorf("list site publications: %w", err))
		return finish()
	}
	publishedSites := make([]domain.Site, 0, len(publications))
	for _, publication := range publications {
		publishedSites = append(publishedSites, publication.Site)
	}
	errorsFound = append(errorsFound, m.reconcileSiteHealth(roundCtx, publishedSites, nodes)...)
	if roundCtx.Err() != nil {
		errorsFound = append(errorsFound, m.roundContextError(roundCtx))
		return finish()
	}
	draftsByID := make(map[string]domain.Site, len(drafts))
	publishedByID := make(map[string]bool, len(publications))
	for _, publication := range publications {
		publishedByID[publication.Site.ID] = true
	}
	for _, draft := range drafts {
		draftsByID[draft.ID] = draft
		if draft.Deleting || (!draft.Enabled && !publishedByID[draft.ID]) {
			if err := m.clearSiteDNS(roundCtx, draft); err != nil {
				errorsFound = append(errorsFound, err)
				continue
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
			if err := m.clearSiteDNS(roundCtx, site); err != nil {
				errorsFound = append(errorsFound, err)
				continue
			}
			m.clearNoHealthyAlert(site.ID)
			continue
		}
		if err := m.reconcileSiteDNS(roundCtx, site, nodes); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	if errors.Is(roundCtx.Err(), context.DeadlineExceeded) {
		errorsFound = append(errorsFound, fmt.Errorf("health reconciliation exceeded %s: %w", m.roundTimeout(), context.DeadlineExceeded))
	}
	return finish()
}

type nodeHealthProbe struct {
	node      domain.Node
	prior     storeNodeHealth
	healthy   bool
	detail    string
	completed bool
}

// storeNodeHealth keeps the worker input independent from SQLite writes.
type storeNodeHealth struct {
	dnsEligible bool
}

func (m *HealthManager) reconcileNodeHealth(ctx context.Context, nodes []domain.Node) []error {
	probes := make([]nodeHealthProbe, 0, len(nodes))
	errorsFound := make([]error, 0)
	for _, node := range nodes {
		if node.Status == domain.NodeRevoked || node.Status == domain.NodeDraining || node.Status == domain.NodeUninstalling || node.Status == domain.NodeUninstalled {
			continue
		}
		prior, err := m.Server.Store.NodeHealth(node.ID)
		if err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("read node health for %s: %w", node.Name, err))
			continue
		}
		probes = append(probes, nodeHealthProbe{node: node, prior: storeNodeHealth{dnsEligible: prior.DNSEligible}})
	}
	m.runBounded(ctx, len(probes), func(index int) {
		healthy, detail := m.checkNode(ctx, probes[index].node)
		if ctx.Err() != nil {
			return
		}
		probes[index].healthy = healthy
		probes[index].detail = detail
		probes[index].completed = true
	})
	for _, probe := range probes {
		if err := ctx.Err(); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("commit node health probes: %w", context.Cause(ctx)))
			break
		}
		if !probe.completed {
			errorsFound = append(errorsFound, fmt.Errorf("probe node %s: %w", probe.node.Name, context.Cause(ctx)))
			continue
		}
		health, err := m.Server.Store.RecordNodeHealth(probe.node.ID, probe.healthy, probe.detail)
		if err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("record node health for %s: %w", probe.node.Name, err))
			continue
		}
		if probe.prior.dnsEligible && !health.DNSEligible && m.Server.Notifier != nil {
			if err := m.Server.Notifier.Notify(ctx, "CDN alert: edge node removed from DNS pool", "Node "+probe.node.Name+" ("+probe.node.PublicIPv4+") failed three consecutive health checks: "+probe.detail); err != nil {
				errorsFound = append(errorsFound, fmt.Errorf("notify node health failure for %s: %w", probe.node.Name, err))
			}
		}
	}
	return errorsFound
}

type siteHealthProbe struct {
	site      domain.Site
	node      domain.Node
	priorDNS  bool
	healthy   bool
	detail    string
	completed bool
}

func (m *HealthManager) reconcileSiteHealth(ctx context.Context, sites []domain.Site, nodes []domain.Node) []error {
	nodesByID := make(map[string]domain.Node, len(nodes))
	for _, node := range nodes {
		nodesByID[node.ID] = node
	}
	probes := make([]siteHealthProbe, 0)
	errorsFound := make([]error, 0)
	for _, site := range sites {
		if !site.Enabled {
			continue
		}
		for _, nodeID := range site.Nodes {
			node, found := nodesByID[nodeID]
			if !found || node.Status != domain.NodeActive {
				continue
			}
			desiredVersion, err := m.Server.Store.DesiredVersion(node.ID)
			if err != nil {
				errorsFound = append(errorsFound, fmt.Errorf("read desired version for site %s on %s: %w", site.Name, node.Name, err))
				continue
			}
			if desiredVersion == 0 || node.AppliedVersion < desiredVersion {
				continue
			}
			nodeHealth, err := m.Server.Store.NodeHealth(node.ID)
			if err != nil {
				errorsFound = append(errorsFound, fmt.Errorf("read node health for site %s on %s: %w", site.Name, node.Name, err))
				continue
			}
			if !nodeHealth.DNSEligible || nodeHealth.LastError != "" {
				continue
			}
			state, _, err := m.Server.Store.NodeState(node.ID)
			if err != nil {
				errorsFound = append(errorsFound, fmt.Errorf("read desired state for site %s on %s: %w", site.Name, node.Name, err))
				continue
			}
			if !nginx.HasSiteHealth(state.NginxConfig, site.ID) {
				continue
			}
			prior, err := m.Server.Store.SiteNodeHealth(site.ID, node.ID)
			if err != nil {
				errorsFound = append(errorsFound, fmt.Errorf("read site health for %s on %s: %w", site.Name, node.Name, err))
				continue
			}
			probes = append(probes, siteHealthProbe{site: site, node: node, priorDNS: prior.DNSEligible})
		}
	}
	m.runBounded(ctx, len(probes), func(index int) {
		healthy, detail := m.siteCheck(ctx, probes[index].site, probes[index].node)
		if ctx.Err() != nil {
			return
		}
		probes[index].healthy = healthy
		probes[index].detail = detail
		probes[index].completed = true
	})
	for _, probe := range probes {
		if err := ctx.Err(); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("commit site health probes: %w", context.Cause(ctx)))
			break
		}
		if !probe.completed {
			errorsFound = append(errorsFound, fmt.Errorf("probe site %s on %s: %w", probe.site.Name, probe.node.Name, context.Cause(ctx)))
			continue
		}
		health, err := m.Server.Store.RecordSiteNodeHealth(probe.site.ID, probe.node.ID, probe.healthy, probe.detail)
		if err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("record site health for %s on %s: %w", probe.site.Name, probe.node.Name, err))
			continue
		}
		if probe.priorDNS && !health.DNSEligible && m.Server.Notifier != nil {
			if err := m.Server.Notifier.Notify(ctx, "CDN alert: site endpoint removed from DNS pool", "Site "+probe.site.Name+" on node "+probe.node.Name+" ("+probe.node.PublicIPv4+") failed three consecutive HTTPS/SNI health checks: "+health.LastError); err != nil {
				errorsFound = append(errorsFound, fmt.Errorf("notify site health failure for %s on %s: %w", probe.site.Name, probe.node.Name, err))
			}
		}
	}
	return errorsFound
}

func (m *HealthManager) runBounded(ctx context.Context, count int, work func(int)) {
	if count == 0 {
		return
	}
	workers := m.workerLimit()
	if workers > count {
		workers = count
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				work(index)
			}
		}()
	}
	for index := 0; index < count; index++ {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			group.Wait()
			return
		}
	}
	close(jobs)
	group.Wait()
}

func (m *HealthManager) workerLimit() int {
	if m.WorkerLimit > 0 {
		return m.WorkerLimit
	}
	return defaultHealthWorkerLimit
}

func (m *HealthManager) roundTimeout() time.Duration {
	if m.RoundTimeout > 0 {
		return m.RoundTimeout
	}
	return defaultHealthRoundTimeout
}

func (m *HealthManager) interval() time.Duration {
	if m.Interval > 0 {
		return m.Interval
	}
	return defaultHealthInterval
}

func (m *HealthManager) roundContextError(ctx context.Context) error {
	cause := context.Cause(ctx)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("health reconciliation exceeded %s: %w", m.roundTimeout(), cause)
	}
	return fmt.Errorf("health reconciliation canceled: %w", cause)
}

func (m *HealthManager) recordRound(started time.Time, errorCount int, timedOut bool, err error) {
	finished := time.Now().UTC()
	status := HealthRoundStatus{
		StartedAt: started, FinishedAt: finished, DurationMS: finished.Sub(started).Milliseconds(),
		ErrorCount: errorCount, TimedOut: timedOut,
	}
	if err != nil {
		status.Error = err.Error()
	}
	m.statusMu.Lock()
	m.lastRound = status
	m.statusMu.Unlock()
	if m.Server == nil || m.Server.Logger == nil {
		return
	}
	if err != nil {
		m.Server.Logger.Error("health reconciliation completed", "duration_ms", status.DurationMS, "errors", errorCount, "timed_out", timedOut, "error", err)
		return
	}
	m.Server.Logger.Info("health reconciliation completed", "duration_ms", status.DurationMS, "errors", 0, "timed_out", false)
}

func (m *HealthManager) LastRound() HealthRoundStatus {
	m.statusMu.RLock()
	defer m.statusMu.RUnlock()
	return m.lastRound
}

func (m *HealthManager) clearSiteDNS(ctx context.Context, site domain.Site) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if err := m.Server.DNS.Reconcile(ctx, site.ZoneID, "site="+site.ID, nil); err != nil {
		return fmt.Errorf("remove DNS for disabled site %s: %w", site.Name, err)
	}
	return nil
}

func (m *HealthManager) reconcileSite(ctx context.Context, site domain.Site, nodes []domain.Node) error {
	healthErrors := m.reconcileSiteHealth(ctx, []domain.Site{site}, nodes)
	dnsErr := m.reconcileSiteDNS(ctx, site, nodes)
	return errors.Join(append(healthErrors, dnsErr)...)
}

func (m *HealthManager) reconcileSiteDNS(ctx context.Context, site domain.Site, nodes []domain.Node) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
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
		if hasSiteHealth {
			siteHealth, err := m.Server.Store.SiteNodeHealth(site.ID, node.ID)
			if err != nil {
				return err
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
			if err := m.Server.Notifier.Notify(ctx, "CDN alert: no healthy nodes for "+site.Name, "DNS was left unchanged because every assigned node is unhealthy. Investigate edge reachability and control-plane probes."); err != nil {
				return fmt.Errorf("notify empty healthy pool for %s: %w", site.Name, err)
			}
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
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
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
