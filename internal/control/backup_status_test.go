package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

func TestBackupRunStatusAtomicRoundTrip(t *testing.T) {
	startedAt := time.Date(2026, 7, 17, 1, 2, 3, 0, time.FixedZone("CST", 8*60*60))
	updatedAt := startedAt.Add(2 * time.Minute)
	status, err := NewBackupRunStatus(BackupRunFailed, 3, 3, "control-1", startedAt, updatedAt, "restic failed")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "nested", "backup.json")
	if err := WriteBackupRunStatus(path, status); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBackupRunStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != BackupRunFailed || got.Attempt != 3 || got.Error != "restic failed" || got.FinishedAt == nil {
		t.Fatalf("status = %#v", got)
	}
	if got.StartedAt.Location() != time.UTC || !got.StartedAt.Equal(startedAt) {
		t.Fatalf("started_at = %v", got.StartedAt)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("status mode = %o", info.Mode().Perm())
	}
}

func TestBackupRunStatusValidationAndDetailLimit(t *testing.T) {
	now := time.Now().UTC()
	if _, err := NewBackupRunStatus(BackupRunFailed, 1, 3, "", now, now, ""); err == nil {
		t.Fatal("accepted failed status without an error")
	}
	status, err := NewBackupRunStatus(BackupRunRetrying, 1, 3, "", now, now, strings.Repeat("x", 5000))
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Error) != 4096 {
		t.Fatalf("error length = %d", len(status.Error))
	}
	status.FinishedAt = &now
	if err := status.Validate(); err == nil {
		t.Fatal("accepted finish time for a retrying status")
	}
	skipped, err := NewBackupRunStatus(BackupRunSkipped, 1, 3, "", now, now, "")
	if err != nil || skipped.FinishedAt == nil {
		t.Fatalf("skipped backup status = %#v, %v", skipped, err)
	}
}

func TestUnreadableBackupStatusBecomesMessage(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	path := filepath.Join(t.TempDir(), "backup.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := Server{Store: database, BackupStatusPath: path}
	if err := server.reconcileMessages(); err != nil {
		t.Fatal(err)
	}
	page, err := database.Messages(50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].Severity != domain.MessageError || page.Messages[0].SourceType != "backup_status" {
		t.Fatalf("backup status diagnostic = %#v", page)
	}
}
