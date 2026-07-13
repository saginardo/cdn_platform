package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestNodeUninstallLifecycleCleansPlatformState(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	node, err := database.CreateNode("edge-remove", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := database.CreateNode("edge-keep", "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(replacement.ID, domain.NodeActive); err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name:          "site",
		Domains:       []string{"cdn.example.test"},
		Nodes:         []string{node.ID, replacement.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteNode(node.ID); !errors.Is(err, ErrNodeAssigned) {
		t.Fatalf("assigned node deletion = %v", err)
	}
	if err := database.SetNodeCertificate(node.ID, "sha256:edge-remove"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateEnrollmentToken(node.ID, "unused-enrollment", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := database.SaveNodeState(node.ID, domain.DesiredState{Version: 4, NginxConfig: "events {}", PublicPorts: []int{80, 443}}, []byte("encrypted")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RecordNodeHealth(node.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO dns_bindings(id, site_id, node_id, domain_name, provider_record_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"binding", site.ID, node.ID, "cdn.example.test", "record", stamp(now()), stamp(now())); err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeDraining); err != nil {
		t.Fatal(err)
	}
	prepared, err := database.PrepareNodeUninstall(node.ID, nil, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.AffectedSiteIDs) != 1 || prepared.AffectedSiteIDs[0] != site.ID {
		t.Fatalf("derived affected sites = %#v", prepared.AffectedSiteIDs)
	}
	site.Nodes = []string{replacement.ID}
	if _, err := database.UpdateSite(site, "zone"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSite(domain.Site{
		Name:          "late-assignment",
		Domains:       []string{"late.example.test"},
		Nodes:         []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       true,
	}, "zone"); err == nil {
		t.Fatal("node with active uninstall workflow was assigned to a new site")
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); !errors.Is(err, ErrUninstallActive) {
		t.Fatalf("active status during uninstall = %v", err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodePending); !errors.Is(err, ErrUninstallActive) {
		t.Fatalf("pending status during uninstall = %v", err)
	}
	if err := database.CreateEnrollmentToken(node.ID, "late-enrollment", time.Now().Add(time.Hour)); !errors.Is(err, ErrUninstallActive) {
		t.Fatalf("enrollment token during uninstall = %v", err)
	}
	if _, err := database.ConsumeEnrollmentToken("unused-enrollment"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("preexisting enrollment token during uninstall = %v", err)
	}
	if err := database.SetNodeCertificate(node.ID, "sha256:late-certificate"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("certificate update during uninstall = %v", err)
	}
	if _, err := database.IssueNodeUninstallToken(node.ID, "uninstall-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var storedToken string
	if err := database.db.QueryRow(`SELECT token_hash FROM node_uninstall_jobs WHERE node_id = ?`, node.ID).Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if storedToken == "uninstall-token" || storedToken == "" {
		t.Fatalf("uninstall token was not stored as a hash: %q", storedToken)
	}

	job, err := database.StartNodeUninstall("uninstall-token")
	if err != nil || job.Status != NodeUninstallRunning {
		t.Fatalf("start uninstall = %#v, %v", job, err)
	}
	startedNode, err := database.GetNode(node.ID)
	if err != nil || startedNode.Status != domain.NodeUninstalling {
		t.Fatalf("started node = %#v, %v", startedNode, err)
	}
	if _, err := database.CancelNodeUninstall(node.ID); !errors.Is(err, ErrUninstallActive) {
		t.Fatalf("running uninstall was canceled: %v", err)
	}
	if _, err := database.NodeIDByFingerprint("sha256:edge-remove"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("uninstalling certificate remained authorized: %v", err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); err == nil {
		t.Fatal("uninstalling node was reactivated")
	}

	job, err = database.FailNodeUninstall("uninstall-token", "nginx validation failed")
	if err != nil || job.Status != NodeUninstallFailed || job.Detail != "nginx validation failed" {
		t.Fatalf("fail uninstall = %#v, %v", job, err)
	}
	failedNode, err := database.GetNode(node.ID)
	if err != nil || failedNode.Status != domain.NodeDraining || failedNode.LastError != "nginx validation failed" {
		t.Fatalf("failed node = %#v, %v", failedNode, err)
	}
	if got, err := database.NodeIDByFingerprint("sha256:edge-remove"); err != nil || got != node.ID {
		t.Fatalf("rolled-back certificate authorization = %q, %v", got, err)
	}
	if _, err := database.CompleteNodeUninstall("uninstall-token"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("failed job completed without restart: %v", err)
	}
	if _, err := database.StartNodeUninstall("uninstall-token"); err != nil {
		t.Fatal(err)
	}
	job, err = database.CompleteNodeUninstall("uninstall-token")
	if err != nil || job.Status != NodeUninstallSucceeded || job.CompletedAt == nil || job.Forced {
		t.Fatalf("complete uninstall = %#v, %v", job, err)
	}
	completedNode, err := database.GetNode(node.ID)
	if err != nil || completedNode.Status != domain.NodeUninstalled || completedNode.LastError != "" {
		t.Fatalf("completed node = %#v, %v", completedNode, err)
	}
	if _, _, err := database.NodeState(node.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("node state was retained: %v", err)
	}
	for _, table := range []string{"enrollment_tokens", "node_health", "dns_bindings"} {
		var count int
		if err := database.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE node_id = ?`, node.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s retained %d rows", table, count)
		}
	}
	if _, err := database.CompleteNodeUninstall("uninstall-token"); err != nil {
		t.Fatalf("completion callback was not idempotent: %v", err)
	}
	if _, err := database.db.Exec(`UPDATE node_uninstall_jobs SET token_expires_at = ? WHERE node_id = ?`, stamp(time.Now().Add(-time.Hour)), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CompleteNodeUninstall("uninstall-token"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired completed token = %v", err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); err == nil {
		t.Fatal("uninstalled node was reactivated")
	}
	if err := database.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetNode(node.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted node lookup = %v", err)
	}
}

func TestNodeUninstallTokenExpiryAndCancellation(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-1", "203.0.113.20")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeDraining); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PrepareNodeUninstall(node.ID, nil, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.IssueNodeUninstallToken(node.ID, "expired", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.StartNodeUninstall("expired"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired start token = %v", err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeRevoked); err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeDraining); !errors.Is(err, ErrUninstallActive) {
		t.Fatalf("revoked workflow changed back to draining: %v", err)
	}
	if _, err := database.CancelNodeUninstall(node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CancelNodeUninstall(node.ID); !errors.Is(err, ErrUninstallNotActive) {
		t.Fatalf("repeated cancellation = %v", err)
	}
	if _, err := database.StartNodeUninstall("expired"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("canceled token = %v", err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeActive); err != nil {
		t.Fatalf("reactivate after cancel: %v", err)
	}
}

func TestForceCompleteNodeUninstallWithoutRemoteToken(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("offline-edge", "203.0.113.30")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeRevoked); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PrepareNodeUninstall(node.ID, nil, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	job, err := database.ForceCompleteNodeUninstall(node.ID, "remote host unavailable")
	if err != nil || job.Status != NodeUninstallForced || !job.Forced || job.CompletedAt == nil {
		t.Fatalf("force complete = %#v, %v", job, err)
	}
	got, err := database.GetNode(node.ID)
	if err != nil || got.Status != domain.NodeUninstalled {
		t.Fatalf("forced node = %#v, %v", got, err)
	}
	if _, err := database.ForceCompleteNodeUninstall(node.ID, "repeat"); !errors.Is(err, ErrUninstallNotActive) {
		t.Fatalf("repeated force completion = %v", err)
	}
}

func TestLateRemoteCallbackVerifiesForcedNodeUninstall(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("late-edge", "203.0.113.31")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeStatus(node.ID, domain.NodeDraining); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PrepareNodeUninstall(node.ID, nil, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.IssueNodeUninstallToken(node.ID, "late-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	forced, err := database.ForceCompleteNodeUninstall(node.ID, "host was unreachable")
	if err != nil || forced.Status != NodeUninstallForced {
		t.Fatalf("force complete = %#v, %v", forced, err)
	}
	if _, err := database.CompleteNodeUninstall("late-token"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("forced uninstall completed without a late start callback: %v", err)
	}
	started, err := database.StartNodeUninstall("late-token")
	if err != nil || started.Status != NodeUninstallForced || started.StartedAt == nil {
		t.Fatalf("late start = %#v, %v", started, err)
	}
	completed, err := database.CompleteNodeUninstall("late-token")
	if err != nil || completed.Status != NodeUninstallSucceeded || completed.Forced {
		t.Fatalf("late completion = %#v, %v", completed, err)
	}
}

func TestStartAndCancelNodeUninstallAreAtomic(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for iteration := range 20 {
		node, err := database.CreateNode(fmt.Sprintf("edge-race-%d", iteration), fmt.Sprintf("203.0.113.%d", 100+iteration))
		if err != nil {
			t.Fatal(err)
		}
		if err := database.SetNodeStatus(node.ID, domain.NodeDraining); err != nil {
			t.Fatal(err)
		}
		if _, err := database.PrepareNodeUninstall(node.ID, nil, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		token := fmt.Sprintf("race-token-%d", iteration)
		if _, err := database.IssueNodeUninstallToken(node.ID, token, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		var startErr, cancelErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, startErr = database.StartNodeUninstall(token)
		}()
		go func() {
			defer wait.Done()
			<-start
			_, cancelErr = database.CancelNodeUninstall(node.ID)
		}()
		close(start)
		wait.Wait()

		job, err := database.NodeUninstallJob(node.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.GetNode(node.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch job.Status {
		case NodeUninstallCanceled:
			if cancelErr != nil || !errors.Is(startErr, ErrTokenInvalid) || got.Status != domain.NodeDraining {
				t.Fatalf("canceled race = job %#v, node %#v, start %v, cancel %v", job, got, startErr, cancelErr)
			}
		case NodeUninstallRunning:
			if startErr != nil || !errors.Is(cancelErr, ErrUninstallActive) || got.Status != domain.NodeUninstalling {
				t.Fatalf("running race = job %#v, node %#v, start %v, cancel %v", job, got, startErr, cancelErr)
			}
		default:
			t.Fatalf("unexpected race result = %#v, start %v, cancel %v", job, startErr, cancelErr)
		}
	}
}
