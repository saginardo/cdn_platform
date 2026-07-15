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
