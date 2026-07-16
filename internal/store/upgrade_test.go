package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestNodeUpgradeTaskLifecycleAndRetryGuard(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("edge-upgrade", "203.0.113.90")
	if err != nil {
		t.Fatal(err)
	}
	sourceDigest := strings.Repeat("1", 64)
	targetDigest := strings.Repeat("2", 64)
	if err := database.HeartbeatWithAgent(node.ID, 0, "", nil, sourceDigest, ""); err != nil {
		t.Fatal(err)
	}
	instruction := testUpgradeInstruction(targetDigest)
	task, created, err := database.CreateOrGetNodeUpgrade(node.ID, instruction, time.Now().Add(time.Hour))
	if err != nil || !created || task.SourceSHA256 != sourceDigest || task.TargetSHA256 != targetDigest {
		t.Fatalf("create task = %#v, created=%v, err=%v", task, created, err)
	}
	duplicate, created, err := database.CreateOrGetNodeUpgrade(node.ID, instruction, time.Now().Add(time.Hour))
	if err != nil || created || duplicate.ID != task.ID {
		t.Fatalf("duplicate task = %#v, created=%v, err=%v", duplicate, created, err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "upgrade-guard", Domains: []string{"guard.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: false,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.CommitSitePublication(site.ID, site.ConfigVersion, "", []NodeStateUpdate{{
		NodeID: node.ID, State: domain.DesiredState{Version: 1, NginxConfig: "events {}"},
	}}, nil)
	if !errors.Is(err, ErrNodeUpgradeActive) {
		t.Fatalf("publication during node upgrade = %v", err)
	}
	applying, err := database.RecordNodeUpgradeReport(node.ID, domain.NodeUpgradeReport{TaskID: task.ID, Status: domain.NodeUpgradeApplying, Detail: "verified"})
	if err != nil || applying.Status != domain.NodeUpgradeApplying || applying.StartedAt == nil {
		t.Fatalf("applying task = %#v, err=%v", applying, err)
	}
	if _, err := database.RecordNodeUpgradeReport(node.ID, domain.NodeUpgradeReport{TaskID: task.ID, Status: domain.NodeUpgradeSucceeded, InstalledSHA256: sourceDigest}); err == nil {
		t.Fatal("accepted a success report with the wrong installed digest")
	}
	failed, err := database.RecordNodeUpgradeReport(node.ID, domain.NodeUpgradeReport{TaskID: task.ID, Status: domain.NodeUpgradeFailed, ErrorCode: "installer_failed", Detail: "rolled back"})
	if err != nil || failed.Status != domain.NodeUpgradeFailed || failed.CompletedAt == nil {
		t.Fatalf("failed task = %#v, err=%v", failed, err)
	}
	if err := database.HeartbeatWithAgent(node.ID, 0, "", nil, sourceDigest, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateOrGetNodeUpgrade(node.ID, instruction, time.Now().Add(time.Hour)); !errors.Is(err, ErrUpgradeRetryNotReady) {
		t.Fatalf("retry while local task active = %v", err)
	}
	if err := database.HeartbeatWithAgent(node.ID, 0, "", nil, sourceDigest, ""); err != nil {
		t.Fatal(err)
	}
	retry, created, err := database.CreateOrGetNodeUpgrade(node.ID, instruction, time.Now().Add(time.Hour))
	if err != nil || !created || retry.ID == task.ID {
		t.Fatalf("retry task = %#v, created=%v, err=%v", retry, created, err)
	}
}

func TestNodeUpgradeRejectsCrossNodeReportAndReconcilesTimeout(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	first, _ := database.CreateNode("edge-first", "203.0.113.91")
	second, _ := database.CreateNode("edge-second", "203.0.113.92")
	digest := strings.Repeat("3", 64)
	for _, node := range []domain.Node{first, second} {
		if err := database.HeartbeatWithAgent(node.ID, 0, "", nil, strings.Repeat("4", 64), ""); err != nil {
			t.Fatal(err)
		}
	}
	task, _, err := database.CreateOrGetNodeUpgrade(first.ID, testUpgradeInstruction(digest), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.RecordNodeUpgradeReport(second.ID, domain.NodeUpgradeReport{TaskID: task.ID, Status: domain.NodeUpgradeApplying}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-node report = %v", err)
	}
	if _, err := database.db.Exec(`UPDATE node_upgrade_tasks SET deadline_at = ? WHERE id = ?`, stamp(time.Now().Add(-time.Minute)), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileNodeUpgrades(); err != nil {
		t.Fatal(err)
	}
	timedOut, err := database.NodeUpgradeTask(task.ID)
	if err != nil || timedOut.Status != domain.NodeUpgradeFailed || timedOut.ErrorCode != "upgrade_timeout" {
		t.Fatalf("timed out task = %#v, err=%v", timedOut, err)
	}
	late, err := database.RecordNodeUpgradeReport(first.ID, domain.NodeUpgradeReport{
		TaskID: task.ID, Status: domain.NodeUpgradeSucceeded, Detail: "completed late", InstalledSHA256: digest,
	})
	if err != nil || late.Status != domain.NodeUpgradeSucceeded {
		t.Fatalf("late success = %#v, err=%v", late, err)
	}
}

func testUpgradeInstruction(targetDigest string) domain.NodeUpgradeInstruction {
	return domain.NodeUpgradeInstruction{
		Binary:         domain.UpgradeArtifact{URL: "https://control.example.test/edge", SHA256: targetDigest},
		Installer:      domain.UpgradeArtifact{URL: "https://control.example.test/install", SHA256: strings.Repeat("a", 64)},
		AgentService:   domain.UpgradeArtifact{URL: "https://control.example.test/agent-service", SHA256: strings.Repeat("b", 64)},
		UpdaterService: domain.UpgradeArtifact{URL: "https://control.example.test/updater-service", SHA256: strings.Repeat("c", 64)},
	}
}
