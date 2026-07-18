package store

import (
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestMessageCenterReconcilesTaskTransitionsAndReadState(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("message-edge", "203.0.113.211")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "message-site", Domains: []string{"message.example.test"},
		Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	task, err := database.CreateTask("publish_site", site.ID, "building configuration")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	page, err := database.Messages(50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].SourceStatus != string(domain.TaskQueued) || page.UnreadCount != 1 {
		t.Fatalf("queued messages = %#v", page)
	}
	if err := database.UpdateTask(task.ID, domain.TaskSucceeded, "edge confirmed"); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	page, err = database.Messages(50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].SourceStatus != string(domain.TaskSucceeded) || page.UnreadCount != 2 {
		t.Fatalf("terminal messages = %#v", page)
	}
	if err := database.MarkMessageRead(page.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	page, err = database.Messages(50, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.UnreadCount != 1 {
		t.Fatalf("unread messages = %#v", page)
	}
	if err := database.MarkAllMessagesRead(); err != nil {
		t.Fatal(err)
	}
	page, err = database.Messages(50, false)
	if err != nil {
		t.Fatal(err)
	}
	if page.UnreadCount != 0 {
		t.Fatalf("unread count = %d", page.UnreadCount)
	}
	if err := database.DeleteMessage(page.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	page, err = database.Messages(50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("dismissed task message was recreated: %#v", page.Messages)
	}
}

func TestCreateMessageOnceDeduplicatesExternalState(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	message := domain.Message{
		Severity: domain.MessageError, Category: "backup", Title: "Backup failed",
		SourceType: "backup", SourceID: "run-1", SourceStatus: "failed",
	}
	first, created, err := database.CreateMessageOnce(message)
	if err != nil || !created {
		t.Fatalf("create message = %#v, %v, %v", first, created, err)
	}
	second, created, err := database.CreateMessageOnce(message)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("deduplicate message = %#v, %v, %v", second, created, err)
	}
	if err := database.DeleteMessage(first.ID); err != nil {
		t.Fatal(err)
	}
	third, created, err := database.CreateMessageOnce(message)
	if err != nil || created || third.ID != first.ID {
		t.Fatalf("dismissed external state was recreated = %#v, %v, %v", third, created, err)
	}
	page, err := database.Messages(50, false)
	if err != nil || len(page.Messages) != 0 || page.UnreadCount != 0 {
		t.Fatalf("dismissed external message remains visible = %#v, %v", page, err)
	}
}

func TestMessageRetentionDoesNotRecreateOldTaskState(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	task, err := database.CreateTask("publish_site", "", "old task")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	page, err := database.Messages(50, false)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("initial messages = %#v, %v", page, err)
	}
	old := stamp(now().AddDate(0, -4, 0))
	if _, err := database.db.Exec(`UPDATE deployment_tasks SET updated_at = ? WHERE id = ?`, old, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE messages SET created_at = ?, read_at = ? WHERE id = ?`, old, old, page.Messages[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	page, err = database.Messages(50, false)
	if err != nil || len(page.Messages) != 0 || page.UnreadCount != 0 {
		t.Fatalf("old task message was recreated = %#v, %v", page, err)
	}
}

func TestNodeUninstallMessagesDistinguishRepeatedLifecycles(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("repeat-uninstall", "203.0.113.219")
	if err != nil {
		t.Fatal(err)
	}
	first := now()
	if _, err := database.db.Exec(`INSERT INTO node_uninstall_jobs(
		node_id, status, previous_status, ready_at, affected_site_ids_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, '[]', ?, ?)`, node.ID, NodeUninstallPreparing, domain.NodeActive, stamp(first), stamp(first), stamp(first)); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	second := first.Add(time.Second)
	if _, err := database.db.Exec(`UPDATE node_uninstall_jobs SET created_at = ?, updated_at = ? WHERE node_id = ?`, stamp(second), stamp(second), node.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.ReconcileTaskMessages(); err != nil {
		t.Fatal(err)
	}
	page, err := database.Messages(50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].SourceID == page.Messages[1].SourceID {
		t.Fatalf("uninstall lifecycle messages = %#v", page.Messages)
	}
}
