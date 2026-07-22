package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"simple_cdn/internal/domain"
)

func TestSiteDeletionLifecycleRetainsTaskAndAuditHistory(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
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
	if err := database.SaveCertificate(site.ID, []byte("certificate"), []byte("key"), nil); err != nil {
		t.Fatal(err)
	}

	deleting, task, created, err := database.BeginSiteDeletion(site.ID, "admin", "127.0.0.1", time.Now().Add(time.Minute))
	if err != nil || !created {
		t.Fatalf("begin deletion = %#v %#v %t %v", deleting, task, created, err)
	}
	if !deleting.Deleting || deleting.Enabled {
		t.Fatalf("site was not disabled for deletion: %#v", deleting)
	}
	_, repeated, created, err := database.BeginSiteDeletion(site.ID, "admin", "127.0.0.1", time.Now().Add(time.Minute))
	if err != nil || created || repeated.ID != task.ID {
		t.Fatalf("repeated deletion = %#v %t %v", repeated, created, err)
	}
	state := domain.DesiredState{Version: 1, NginxConfig: "events {}", PublicPorts: []int{80}}
	if err := database.StageSiteDeletion(task.ID,
		[]NodeStateUpdate{{NodeID: node.ID, State: state, CertificatesCiphertext: []byte("encrypted-empty-map")}},
		[]PublishTaskNode{{NodeID: node.ID, TargetVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	report := &domain.ApplyReport{Version: 1, Status: domain.ApplySucceeded, Detail: "site removed"}
	if err := database.Heartbeat(node.ID, 1, "", report); err != nil {
		t.Fatal(err)
	}
	ready, err := database.SiteDeletionReady(task.ID)
	if err != nil || !ready {
		t.Fatalf("deletion ready = %t, %v", ready, err)
	}
	job, err := database.SiteDeletionJobForTask(task.ID)
	if err != nil || job.Phase != SiteDeletionFinalizing {
		t.Fatalf("finalizing job = %#v, %v", job, err)
	}
	if err := database.CompleteSiteDeletion(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.GetSite(site.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted site lookup = %v", err)
	}
	completed, err := database.GetTask(task.ID)
	if err != nil || completed.Status != domain.TaskSucceeded {
		t.Fatalf("retained deletion task = %#v, %v", completed, err)
	}
	for _, table := range []string{"certificates", "site_domains", "site_deletion_jobs"} {
		var count int
		if err := database.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE site_id = ?`, site.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d site rows", table, count)
		}
	}
	var audits int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE resource_type = 'site' AND resource_id = ? AND action IN ('delete_requested', 'delete')`, site.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("deletion audit count = %d", audits)
	}
}

func TestSiteDeletionTimeoutKeepsDisabledSiteAndCanRetry(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "timeout", Domains: []string{"timeout.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	_, task, _, err := database.BeginSiteDeletion(site.ID, "admin", "127.0.0.1", time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.StageSiteDeletion(task.ID,
		[]NodeStateUpdate{{NodeID: node.ID, State: domain.DesiredState{Version: 1, NginxConfig: "events {}"}, CertificatesCiphertext: []byte("empty")}},
		[]PublishTaskNode{{NodeID: node.ID, TargetVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	if ready, err := database.SiteDeletionReady(task.ID); err != nil || ready {
		t.Fatalf("timed out deletion ready = %t, %v", ready, err)
	}
	failed, err := database.GetTask(task.ID)
	if err != nil || failed.Status != domain.TaskFailed {
		t.Fatalf("timed out task = %#v, %v", failed, err)
	}
	retained, _, err := database.GetSite(site.ID)
	if err != nil || !retained.Deleting || retained.Enabled {
		t.Fatalf("site after timeout = %#v, %v", retained, err)
	}
	_, retry, created, err := database.BeginSiteDeletion(site.ID, "admin", "127.0.0.1", time.Now().Add(time.Minute))
	if err != nil || !created || retry.ID == task.ID {
		t.Fatalf("retry task = %#v %t %v", retry, created, err)
	}
}

func TestSiteDeletionRejectsActivePublishOrCertificateTask(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-pending", "203.0.113.12")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "busy", Domains: []string{"busy.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateOrGetActivePublishTask(site.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := database.BeginSiteDeletion(site.ID, "admin", "127.0.0.1", time.Now().Add(time.Minute)); !errors.Is(err, ErrSiteTaskActive) {
		t.Fatalf("active task deletion error = %v", err)
	}
	loaded, _, err := database.GetSite(site.ID)
	if err != nil || loaded.Deleting || !loaded.Enabled {
		t.Fatalf("busy site changed: %#v, %v", loaded, err)
	}
}
