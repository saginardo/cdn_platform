package control

import (
	"context"
	"path/filepath"
	"testing"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

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
