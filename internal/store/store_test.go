package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestEnrollmentTokenSingleUse(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnrollmentToken(node.ID, "one-time-token", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	nodeID, err := store.ConsumeEnrollmentToken("one-time-token")
	if err != nil || nodeID != node.ID {
		t.Fatalf("unexpected consume: %q %v", nodeID, err)
	}
	if _, err := store.ConsumeEnrollmentToken("one-time-token"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected token error, got %v", err)
	}
}

func TestHealthHysteresis(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		health, err := store.RecordNodeHealth(node.ID, true, "")
		if err != nil || health.DNSEligible {
			t.Fatalf("node became eligible too early: %#v %v", health, err)
		}
	}
	health, err := store.RecordNodeHealth(node.ID, true, "")
	if err != nil || !health.DNSEligible {
		t.Fatalf("node did not become eligible: %#v %v", health, err)
	}
	for range 2 {
		health, err = store.RecordNodeHealth(node.ID, false, "timeout")
		if err != nil || !health.DNSEligible {
			t.Fatalf("node dropped too early: %#v %v", health, err)
		}
	}
	health, err = store.RecordNodeHealth(node.ID, false, "timeout")
	if err != nil || health.DNSEligible {
		t.Fatalf("node did not drop after three failures: %#v %v", health, err)
	}
}

func TestCreateNodeRejectsNonPublicAddress(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "0.0.0.0", "224.0.0.1"} {
		if _, err := store.CreateNode("edge-"+address, address); err == nil {
			t.Fatalf("expected %s to be rejected", address)
		}
	}
}

func TestRevokedNodeCannotReceiveEnrollmentToken(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeStatus(node.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnrollmentToken(node.ID, "token", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("expected enrollment for revoked node to fail")
	}
}

func TestRevokingNodeInvalidatesExistingEnrollmentToken(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEnrollmentToken(node.ID, "token", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeStatus(node.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeEnrollmentToken("token"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected revoked token to be invalid, got %v", err)
	}
}

func TestRevokingNodeInvalidatesItsClientCertificate(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeCertificate(node.ID, "sha256:edge-cert"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeStatus(node.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NodeIDByFingerprint("sha256:edge-cert"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected revoked certificate to be invalid, got %v", err)
	}
	if err := store.SetNodeStatus(node.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NodeIDByFingerprint("sha256:edge-cert"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old certificate revived after reactivation: %v", err)
	}
}

func TestRevokedNodeCannotReceiveCertificateFingerprint(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeStatus(node.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeCertificate(node.ID, "sha256:late-cert"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected revoked node certificate update to fail, got %v", err)
	}
}

func TestOpeningStoreFailsInterruptedCertificateTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := first.CreateTask("issue_certificate", "site", "waiting for DNS-01 validation")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.UpdateTask(task.ID, domain.TaskApplying, "waiting for DNS-01 validation"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	updated, err := second.GetTask(task.ID)
	if err != nil || updated.Status != domain.TaskFailed || !strings.Contains(updated.Detail, "restart") {
		t.Fatalf("recovered task = %#v, err=%v", updated, err)
	}
}

func TestHasSuccessfulPublishAfterCertificateTask(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certificateTask, err := store.CreateTask("issue_certificate", "site", "certificate stored; publish the site to deploy it")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(certificateTask.ID, domain.TaskSucceeded, certificateTask.Detail); err != nil {
		t.Fatal(err)
	}
	completedCertificateTask, err := store.GetTask(certificateTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := store.HasSuccessfulPublishAfter("site", completedCertificateTask.UpdatedAt)
	if err != nil || published {
		t.Fatalf("publish before deployment task = %t, err=%v", published, err)
	}
	time.Sleep(time.Millisecond)
	publishTask, err := store.CreateTask("publish_site", "site", "configuration available to assigned nodes")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(publishTask.ID, domain.TaskSucceeded, publishTask.Detail); err != nil {
		t.Fatal(err)
	}
	published, err = store.HasSuccessfulPublishAfter("site", completedCertificateTask.UpdatedAt)
	if err != nil || !published {
		t.Fatalf("publish after certificate task = %t, err=%v", published, err)
	}
}

func TestPublishTaskTimesOutUnconfirmedNodes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	task, created, err := store.CreateOrGetActivePublishTask("site", time.Now().Add(-time.Second))
	if err != nil || !created {
		t.Fatalf("create publish task: %#v %t %v", task, created, err)
	}
	if err := store.UpdateTask(task.ID, domain.TaskApplying, "waiting for edge"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePublishTaskNodes(task.ID, []PublishTaskNode{{NodeID: node.ID, TargetVersion: 3}}); err != nil {
		t.Fatal(err)
	}
	status, err := store.PublishStatus("site")
	if err != nil {
		t.Fatal(err)
	}
	if status.Task == nil || status.Task.Status != domain.TaskFailed || len(status.Nodes) != 1 || status.Nodes[0].Status != domain.PublishNodeTimedOut || status.Nodes[0].ErrorCode != "confirmation_timeout" {
		t.Fatalf("unexpected timeout status: %#v", status)
	}
}

func TestPublishApplyReportConfirmsAnOlderTargetVersion(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	task, created, err := store.CreateOrGetActivePublishTask("site", time.Now().Add(time.Minute))
	if err != nil || !created {
		t.Fatalf("create task: %#v %t %v", task, created, err)
	}
	if err := store.UpdateTask(task.ID, domain.TaskApplying, "waiting for edge"); err != nil {
		t.Fatal(err)
	}
	if err := store.CreatePublishTaskNodes(task.ID, []PublishTaskNode{{NodeID: node.ID, TargetVersion: 4}}); err != nil {
		t.Fatal(err)
	}
	report := &domain.ApplyReport{Version: 5, Status: domain.ApplySucceeded, Detail: "Nginx is listening on TCP 80 and TCP 443"}
	if err := store.Heartbeat(node.ID, 5, "", report); err != nil {
		t.Fatal(err)
	}
	status, err := store.PublishStatus("site")
	if err != nil {
		t.Fatal(err)
	}
	if status.Task == nil || status.Task.Status != domain.TaskSucceeded || len(status.Nodes) != 1 || status.Nodes[0].Detail != report.Detail {
		t.Fatalf("higher-version apply report did not preserve detail: %#v", status)
	}
}

func TestSiteStreamPathsRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSite(domain.Site{
		Name: "streaming", Domains: []string{"stream.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		StreamPaths:   []string{"/ws/", "/events"}, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := store.GetSite(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.StreamPaths) != 2 || loaded.StreamPaths[0] != "/events" || loaded.StreamPaths[1] != "/ws" {
		t.Fatalf("unexpected stream paths: %#v", loaded.StreamPaths)
	}
}

func TestSitePassthroughRoundTripAndCacheGeneration(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSite(domain.Site{
		Name: "passthrough", Domains: []string{"stream.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Passthrough:   true, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	loaded, zoneID, err := store.GetSite(created.ID)
	if err != nil || !loaded.Passthrough || zoneID != "zone" {
		t.Fatalf("unexpected stored passthrough site: %#v %q %v", loaded, zoneID, err)
	}
	if _, err := store.InvalidateSiteCache(created.ID); !errors.Is(err, ErrCacheDisabled) {
		t.Fatalf("expected cache-disabled error, got %v", err)
	}
	loaded.Passthrough = false
	updated, err := store.UpdateSite(loaded, zoneID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Passthrough || updated.CacheGeneration != created.CacheGeneration+1 {
		t.Fatalf("unexpected cache generation after disabling passthrough: %#v", updated)
	}
	invalidated, err := store.InvalidateSiteCache(updated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if invalidated.CacheGeneration != updated.CacheGeneration+1 {
		t.Fatalf("cache invalidation did not advance generation: %#v", invalidated)
	}
}

func TestSiteClientMaxBodySizeRoundTrip(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	node, err := store.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSite(domain.Site{
		Name: "large-requests", Domains: []string{"api.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin:       domain.Origin{URL: "https://origin.example.test", Enabled: true},
		ClientMaxBodySizeMB: 1024, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	loaded, zoneID, err := store.GetSite(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ClientMaxBodySizeMB != 1024 {
		t.Fatalf("stored client max body size = %d", loaded.ClientMaxBodySizeMB)
	}
	loaded.ClientMaxBodySizeMB = 256
	updated, err := store.UpdateSite(loaded, zoneID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ClientMaxBodySizeMB != 256 || updated.CacheGeneration != created.CacheGeneration || updated.ConfigVersion != created.ConfigVersion+1 {
		t.Fatalf("unexpected updated site: %#v", updated)
	}
}

func TestOpenMigratesSiteColumnsForExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE sites (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  zone_id TEXT NOT NULL,
  domains_json TEXT NOT NULL,
  node_ids_json TEXT NOT NULL,
  primary_origin_json TEXT NOT NULL,
  backup_origin_json TEXT,
  cache_generation INTEGER NOT NULL DEFAULT 1,
  config_version INTEGER NOT NULL DEFAULT 1,
  published INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	_, err = legacy.Exec(`INSERT INTO sites(id, name, zone_id, domains_json, node_ids_json, primary_origin_json, cache_generation, config_version, published, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 1, 1, 0, 1, ?, ?)`,
		"legacy-site", "legacy", "zone", `["legacy.example.test"]`, `["node-1"]`, `{"url":"https://origin.example.test","host_header":"origin.example.test","enabled":true}`, stamp(time.Now()), stamp(time.Now()))
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	rows, err := migrated.db.Query(`PRAGMA table_info(sites)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "stream_paths_json" || name == "passthrough" || name == "client_max_body_size_mb" {
			found[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found["stream_paths_json"] || !found["passthrough"] || !found["client_max_body_size_mb"] {
		t.Fatalf("site columns were not added to legacy table: %#v", found)
	}
	site, _, err := migrated.GetSite("legacy-site")
	if err != nil || site.Passthrough || site.ClientMaxBodySizeMB != domain.DefaultClientMaxBodySizeMB {
		t.Fatalf("legacy site defaults: passthrough=%t client_max_body_size_mb=%d err=%v", site.Passthrough, site.ClientMaxBodySizeMB, err)
	}
}
