package control

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/nginx"
	"cdn-platform/internal/store"
)

type healthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f healthRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func healthyNodeClient() *http.Client {
	return nodeHealthClient(http.StatusOK)
}

func nodeHealthClient(statusCode int) *http.Client {
	return &http.Client{Transport: healthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: statusCode,
			Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
			Body:       io.NopCloser(strings.NewReader("ok\n")),
			Header:     make(http.Header),
		}, nil
	})}
}

func TestDisabledSiteWithdrawsManagedDNSBeforeRepublish(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CreateSite(domain.Site{
		Name:          "disabled-site",
		Domains:       []string{"cdn.example.test"},
		Nodes:         []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       false,
	}, "zone-1")
	if err != nil {
		t.Fatal(err)
	}
	dns := &MemoryDNS{Zones: map[string][]integrations.DNSRecord{
		"zone-1": {{Name: "cdn.example.test", Content: node.PublicIPv4}},
	}}
	manager := HealthManager{Server: &Server{Store: database, DNS: dns}}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := dns.Zones["zone-1"]; len(got) != 0 {
		t.Fatalf("disabled site DNS was not withdrawn: %#v", got)
	}
}

func TestPendingSiteDraftDoesNotChangePublishedHealthOrDNS(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.12")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "published-site", Domains: []string{"old.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone-1")
	if err != nil {
		t.Fatal(err)
	}
	site, err = database.MarkSitePublished(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := nginx.Render([]domain.Site{site})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveNodeState(node.ID, domain.DesiredState{Version: 1, NginxConfig: configuration}, nil); err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(node.ID, 1, "", nil); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := database.RecordNodeHealth(node.ID, true, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := database.RecordSiteNodeHealth(site.ID, node.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	draft, zoneID, err := database.GetSite(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Domains = []string{"draft.example.test"}
	draft.Enabled = false
	draftTTL := 300
	draft.DNSTTLSeconds = &draftTTL
	if _, err := database.UpdateSite(draft, zoneID); err != nil {
		t.Fatal(err)
	}
	key, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := NewSettingsManager(database, cipher, EnvironmentSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.SaveDNSDefaultTTL(120); err != nil {
		t.Fatal(err)
	}
	probedDomain := ""
	dns := &MemoryDNS{}
	manager := HealthManager{
		Server: &Server{Store: database, DNS: dns, Settings: settings},
		Client: healthyNodeClient(),
		SiteProbe: func(_ context.Context, published domain.Site, _ domain.Node) (bool, string) {
			probedDomain = published.Domains[0]
			return true, ""
		},
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probedDomain != "old.example.test" {
		t.Fatalf("health probe used draft domain %q", probedDomain)
	}
	records := dns.Zones["zone-1"]
	if len(records) != 1 || records[0].Name != "old.example.test" || records[0].Content != node.PublicIPv4 || records[0].TTL != 120 {
		t.Fatalf("DNS did not retain the published snapshot: %#v", records)
	}
}

func TestDrainedSitePoolWithdrawsManagedDNS(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name:          "drained-site",
		Domains:       []string{"cdn.example.test"},
		Nodes:         []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       true,
	}, "zone-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.MarkSitePublished(site.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeDraining); err != nil {
		t.Fatal(err)
	}
	dns := &MemoryDNS{Zones: map[string][]integrations.DNSRecord{
		"zone-1": {{Name: "cdn.example.test", Content: node.PublicIPv4}},
	}}
	manager := HealthManager{Server: &Server{Store: database, DNS: dns}}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := dns.Zones["zone-1"]; len(got) != 0 {
		t.Fatalf("drained site DNS was not withdrawn: %#v", got)
	}
}

func TestSiteHTTPSHealthRemovesOnlyFailingNodeAfterThreeChecks(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	badNode, err := database.CreateNode("edge-bad", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	goodNode, err := database.CreateNode("edge-good", "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name:          "site-health",
		Domains:       []string{"cdn.example.test"},
		Nodes:         []string{badNode.ID, goodNode.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       true,
	}, "zone-1")
	if err != nil {
		t.Fatal(err)
	}
	site, err = database.MarkSitePublished(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := nginx.Render([]domain.Site{site})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []domain.Node{badNode, goodNode} {
		if err := database.SaveNodeState(node.ID, domain.DesiredState{Version: 1, NginxConfig: configuration}, nil); err != nil {
			t.Fatal(err)
		}
		if err := database.Heartbeat(node.ID, 1, "", nil); err != nil {
			t.Fatal(err)
		}
		for range 5 {
			if _, err := database.RecordNodeHealth(node.ID, true, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := database.RecordSiteNodeHealth(site.ID, node.ID, true, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	dns := &MemoryDNS{}
	manager := HealthManager{
		Server: &Server{Store: database, DNS: dns},
		Client: healthyNodeClient(),
		SiteProbe: func(_ context.Context, _ domain.Site, node domain.Node) (bool, string) {
			if node.ID == badNode.ID {
				return false, "cdn.example.test: certificate is valid for another site"
			}
			return true, ""
		},
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := manager.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		records := dns.Zones["zone-1"]
		if attempt < 3 && len(records) != 2 {
			t.Fatalf("attempt %d removed a node before the failure threshold: %#v", attempt, records)
		}
	}
	records := dns.Zones["zone-1"]
	if len(records) != 1 || records[0].Content != goodNode.PublicIPv4 {
		t.Fatalf("DNS did not retain only the healthy site endpoint: %#v", records)
	}
	manager.Client = nodeHealthClient(http.StatusServiceUnavailable)
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	records = dns.Zones["zone-1"]
	if len(records) != 1 || records[0].Content != goodNode.PublicIPv4 {
		t.Fatalf("generic probe failure reintroduced a site-ineligible node: %#v", records)
	}
	siteHealth, err := database.SiteNodeHealth(site.ID, badNode.ID)
	if err != nil || siteHealth.DNSEligible || siteHealth.ConsecutiveFailures != 3 {
		t.Fatalf("failing site-node health = %#v, %v", siteHealth, err)
	}
	nodeHealth, err := database.NodeHealth(badNode.ID)
	if err != nil || !nodeHealth.DNSEligible || nodeHealth.ConsecutiveFailures != 1 {
		t.Fatalf("generic node health should remain eligible after one failure: %#v, %v", nodeHealth, err)
	}
}

func TestSiteHTTPSHealthFallsBackDuringLegacyConfigRollout(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-legacy", "203.0.113.20")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name:          "legacy-site",
		Domains:       []string{"legacy.example.test"},
		Nodes:         []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       true,
	}, "zone-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.MarkSitePublished(site.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveNodeState(node.ID, domain.DesiredState{Version: 1, NginxConfig: "# legacy configuration without site health capability\n"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(node.ID, 1, "", nil); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if _, err := database.RecordNodeHealth(node.ID, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	probeCalls := 0
	dns := &MemoryDNS{}
	manager := HealthManager{
		Server: &Server{Store: database, DNS: dns},
		Client: healthyNodeClient(),
		SiteProbe: func(context.Context, domain.Site, domain.Node) (bool, string) {
			probeCalls++
			return false, "must not be called"
		},
	}
	if err := manager.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probeCalls != 0 {
		t.Fatalf("legacy config invoked site probe %d times", probeCalls)
	}
	records := dns.Zones["zone-legacy"]
	if len(records) != 1 || records[0].Content != node.PublicIPv4 {
		t.Fatalf("legacy config did not retain node-level DNS eligibility: %#v", records)
	}
}

func TestTCPOnlySiteKeepsNodeUntilHealthFailureThreshold(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	badNode, err := database.CreateNode("edge-transient", "203.0.113.30")
	if err != nil {
		t.Fatal(err)
	}
	goodNode, err := database.CreateNode("edge-healthy", "203.0.113.31")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "mail", Domains: []string{"mail.example.test"}, Nodes: []string{badNode.ID, goodNode.ID},
		TCPOnly: true, TCPForwards: []domain.TCPForward{{Name: "smtps", ListenPort: 9465, UpstreamHost: "mail-origin.example.test", UpstreamPort: 465}}, Enabled: true,
	}, "zone-mail")
	if err != nil {
		t.Fatal(err)
	}
	site, err = database.MarkSitePublished(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := nginx.Render([]domain.Site{site})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []domain.Node{badNode, goodNode} {
		if err := database.SetNodeStatus(node.ID, domain.NodeActive); err != nil {
			t.Fatal(err)
		}
		if err := database.SaveNodeState(node.ID, domain.DesiredState{Version: 1, NginxConfig: configuration, NginxStreamConfig: "# stream\n", PublicPorts: []int{9465}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := database.Heartbeat(node.ID, 1, "", nil); err != nil {
			t.Fatal(err)
		}
		for range 5 {
			if _, err := database.RecordNodeHealth(node.ID, true, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if health, err := database.RecordNodeHealth(badNode.ID, false, "TCP 9465: timeout"); err != nil || !health.DNSEligible {
		t.Fatalf("transiently failing node health = %#v, %v", health, err)
	}
	nodes, err := database.ListNodes()
	if err != nil {
		t.Fatal(err)
	}
	dns := &MemoryDNS{}
	manager := HealthManager{Server: &Server{Store: database, DNS: dns}}
	if err := manager.reconcileSite(context.Background(), site, nodes); err != nil {
		t.Fatal(err)
	}
	records := dns.Zones["zone-mail"]
	if len(records) != 2 {
		t.Fatalf("transient failure removed TCP endpoint before threshold: %#v", records)
	}
}
