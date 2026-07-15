package control

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

type recordingCertificateCleaner struct {
	names []string
	err   error
}

func (c *recordingCertificateCleaner) Delete(_ context.Context, name string) error {
	c.names = append(c.names, name)
	return c.err
}

func TestSiteDeleteAPIWaitsForEdgeAndThenRemovesSite(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "delete-me", Domains: []string{"delete.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone")
	if err != nil {
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
	dns := &MemoryDNS{Zones: map[string][]integrations.DNSRecord{"zone": {{Name: "delete.example.test", Content: node.PublicIPv4}}}}
	cleaner := &recordingCertificateCleaner{}
	publisher := Publisher{Store: database, Cipher: cipher}
	deleter := &SiteDeletionManager{Store: database, Publisher: publisher, DNS: dns, Certificates: cleaner}
	server := &Server{Store: database, Cipher: cipher, Publisher: publisher, DNS: dns, SiteDeleter: deleter}

	wrong := requestSiteResponse(t, server, http.MethodDelete, "/api/sites/"+site.ID, map[string]any{"confirmation": "wrong"})
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("wrong confirmation = %d %s", wrong.Code, wrong.Body.String())
	}
	accepted := requestSiteResponse(t, server, http.MethodDelete, "/api/sites/"+site.ID, map[string]any{"confirmation": site.Name})
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("delete request = %d %s", accepted.Code, accepted.Body.String())
	}
	retained, _, err := database.GetSite(site.ID)
	if err != nil || !retained.Deleting || retained.Enabled {
		t.Fatalf("site while waiting = %#v, %v", retained, err)
	}
	if len(dns.Zones["zone"]) != 0 {
		t.Fatalf("managed DNS was retained: %#v", dns.Zones["zone"])
	}
	blocked := requestSiteResponse(t, server, http.MethodPut, "/api/sites/"+site.ID, map[string]any{
		"name": site.Name, "zone_id": "zone", "domains": site.Domains, "node_ids": site.Nodes,
		"primary_origin": site.PrimaryOrigin, "enabled": false,
	})
	if blocked.Code != http.StatusConflict {
		t.Fatalf("update during deletion = %d %s", blocked.Code, blocked.Body.String())
	}
	desired, _, err := database.NodeState(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Heartbeat(node.ID, desired.Version, "", &domain.ApplyReport{Version: desired.Version, Status: domain.ApplySucceeded, Detail: "site removed"}); err != nil {
		t.Fatal(err)
	}
	status, err := deleter.Status(context.Background(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Task == nil || status.Task.Status != domain.TaskSucceeded {
		t.Fatalf("completed deletion status = %#v", status)
	}
	if _, _, err := database.GetSite(site.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted site lookup = %v", err)
	}
	if len(cleaner.names) != 1 || cleaner.names[0] != "site-"+site.ID {
		t.Fatalf("certificate cleanup calls = %#v", cleaner.names)
	}
	persisted, err := deleter.Status(context.Background(), site.ID)
	if err != nil || persisted.Task == nil || persisted.Task.Status != domain.TaskSucceeded {
		t.Fatalf("persisted deletion status = %#v, %v", persisted, err)
	}
}

func TestSiteDeleteCertificateCleanupFailureCanBeRetried(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-pending", "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "retry-delete", Domains: []string{"retry.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone")
	if err != nil {
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
	cleaner := &recordingCertificateCleaner{err: errors.New("certbot locked")}
	publisher := Publisher{Store: database, Cipher: cipher}
	deleter := &SiteDeletionManager{Store: database, Publisher: publisher, DNS: &MemoryDNS{}, Certificates: cleaner}
	status, err := deleter.Start(context.Background(), site.ID, "admin", "127.0.0.1")
	if err == nil || status.Task == nil || status.Task.Status != domain.TaskFailed {
		t.Fatalf("failed cleanup = %#v, %v", status, err)
	}
	retained, _, err := database.GetSite(site.ID)
	if err != nil || !retained.Deleting {
		t.Fatalf("site after cleanup failure = %#v, %v", retained, err)
	}
	cleaner.err = nil
	status, err = deleter.Start(context.Background(), site.ID, "admin", "127.0.0.1")
	if err != nil || status.Task == nil || status.Task.Status != domain.TaskSucceeded {
		t.Fatalf("cleanup retry = %#v, %v", status, err)
	}
	if len(cleaner.names) != 2 {
		t.Fatalf("cleanup calls = %#v", cleaner.names)
	}
}
