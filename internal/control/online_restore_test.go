package control

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

type fakeRestoreClickHouse struct {
	mu        sync.Mutex
	databases map[string]bool
}

type responseLostRestoreClickHouse struct {
	*fakeRestoreClickHouse
}

func (f *responseLostRestoreClickHouse) RenameDatabase(ctx context.Context, source, target string) error {
	if err := f.fakeRestoreClickHouse.RenameDatabase(ctx, source, target); err != nil {
		return err
	}
	return errors.New("simulated lost ClickHouse response")
}

func (f *fakeRestoreClickHouse) DatabaseExists(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.databases[name], nil
}

func (f *fakeRestoreClickHouse) RestoreDatabase(_ context.Context, source, target, diskPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if source != "cdn_platform" || !f.databases[source] || !strings.Contains(diskPath, "online-restore/jobs/") {
		return errors.New("invalid fake restore request")
	}
	f.databases[target] = true
	return nil
}

func (f *fakeRestoreClickHouse) ValidateDatabase(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.databases[name] {
		return errors.New("database does not exist")
	}
	return nil
}

func (f *fakeRestoreClickHouse) RenameDatabase(_ context.Context, source, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.databases[source] || f.databases[target] {
		return errors.New("invalid fake database rename")
	}
	delete(f.databases, source)
	f.databases[target] = true
	return nil
}

func (f *fakeRestoreClickHouse) DropDatabase(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.databases, name)
	return nil
}

func TestOnlineRestoreStagesCommitsAndAppliesVerifiedSnapshot(t *testing.T) {
	temporary := t.TempDir()
	fixtureRoot := filepath.Join(temporary, "fixture")
	controlFixture := filepath.Join(fixtureRoot, "backup", "staging", "control")
	clickHouseFixture := filepath.Join(fixtureRoot, "backup", "staging", "clickhouse", "cdn-platform-current")
	if err := os.MkdirAll(controlFixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(clickHouseFixture, 0o700); err != nil {
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
	restoredDatabase, err := store.Open(filepath.Join(controlFixture, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := cipher.Encrypt([]byte("restored-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := restoredDatabase.SetSecret(store.SecretCloudflareAPIToken, ciphertext); err != nil {
		t.Fatal(err)
	}
	if err := restoredDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	restoredSecrets := filepath.Join(temporary, "restored-secrets")
	if _, err := LoadOrCreateInternalCA(filepath.Join(restoredSecrets, "pki")); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreTestArchive(filepath.Join(controlFixture, "control-secrets.tar.gz"), restoredSecrets); err != nil {
		t.Fatal(err)
	}
	emptyTLS := filepath.Join(temporary, "empty-tls")
	if err := os.MkdirAll(emptyTLS, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreTestArchive(filepath.Join(controlFixture, "control-tls.tar.gz"), emptyTLS); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clickHouseFixture, ".backup"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(temporary, "data")
	tlsDir := filepath.Join(temporary, "tls")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	liveDatabase, err := store.Open(filepath.Join(dataDir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := liveDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateInternalCA(filepath.Join(dataDir, "pki")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "old.pem"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshotID := strings.Repeat("a", 64)
	resticPath := filepath.Join(temporary, "restic")
	resticScript := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  snapshots)
    printf '[{"id":"%s","short_id":"aaaaaaaa","time":"2026-07-18T01:02:03Z","tags":["cdn-control-compose"]}]' "$SNAPSHOT_ID"
    ;;
  restore)
    shift
    target=""
    while (($#)); do
      if [[ "$1" == "--target" ]]; then target="$2"; shift 2; else shift; fi
    done
    mkdir -p "$target"
    cp -a "$FIXTURE_ROOT/." "$target/"
    ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(resticPath, []byte(resticScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SNAPSHOT_ID", snapshotID)
	t.Setenv("FIXTURE_ROOT", fixtureRoot)

	settingsDatabase, err := store.Open(filepath.Join(temporary, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer settingsDatabase.Close()
	settings, err := NewSettingsManager(settingsDatabase, cipher, EnvironmentSettings{
		Backup: domain.BackupSettings{
			Repository:  "s3:https://s3.example.test/backups",
			AccessKeyID: "access-key",
			Region:      "us-east-1",
			BackupTime:  "03:25",
		},
		BackupAccessKey: "secret-key",
		BackupPassword:  "repository-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	clickHouse := &fakeRestoreClickHouse{databases: map[string]bool{"cdn_platform": true}}
	restoreRoot := filepath.Join(temporary, "online-restore")
	manager, err := NewOnlineRestoreManager(OnlineRestoreManagerConfig{
		Root:              restoreRoot,
		Settings:          settings,
		Cipher:            cipher,
		ClickHouse:        clickHouse,
		ResticBinary:      resticPath,
		ClickHouseGroupID: -1,
		RestoreTimeout:    10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshots, err := manager.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != snapshotID {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	job, err := manager.Start(snapshotID, snapshotID[:8])
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		current := manager.Current()
		if current != nil && current.State == OnlineRestoreReady {
			job = *current
			break
		}
		if current != nil && current.State == OnlineRestoreFailed {
			t.Fatalf("restore preparation failed: %s", current.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("restore did not become ready: %#v", current)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.DatabaseSHA256 == "" || job.CAFingerprint == "" || job.SchemaVersion != store.LatestSchemaVersion() {
		t.Fatalf("verified job = %#v", job)
	}
	job, err = manager.Commit(job.ID, "RESTORE")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != OnlineRestoreCommitting {
		t.Fatalf("committing job = %#v", job)
	}
	manager.Stop()

	applied, err := ApplyPendingOnlineRestore(context.Background(), OnlineRestoreApplyConfig{
		Root:         restoreRoot,
		DataDir:      dataDir,
		TLSDir:       tlsDir,
		Cipher:       cipher,
		ClickHouse:   clickHouse,
		ApplyTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("pending restore was not applied")
	}
	if got, err := fileSHA256(filepath.Join(dataDir, "control.db")); err != nil || got != job.DatabaseSHA256 {
		t.Fatalf("promoted database hash = %q, err = %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "control.db.before-restore-"+job.ID)); err != nil {
		t.Fatalf("previous SQLite database was not retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tlsDir, "before-restore-"+job.ID, "old.pem")); err != nil {
		t.Fatalf("previous TLS state was not retained: %v", err)
	}
	if exists, _ := clickHouse.DatabaseExists(context.Background(), job.RollbackDatabase); !exists {
		t.Fatal("previous ClickHouse database was not retained")
	}
	if _, err := os.Stat(onlineRestoreMaintenancePath(restoreRoot)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance lock still exists: %v", err)
	}
	completed, err := readOnlineRestoreJob(restoreRoot)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != OnlineRestoreCompleted {
		t.Fatalf("completed job = %#v", completed)
	}
}

func TestExtractRestoreArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "unsafe.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractRestoreArchive(archivePath, filepath.Join(t.TempDir(), "target")); err == nil {
		t.Fatal("accepted path traversal in restore archive")
	}
}

func TestRestorePathSwapResumesAndRollsBackAppliedState(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	staged := filepath.Join(root, "staged")
	backup := filepath.Join(root, "backup")
	if err := os.WriteFile(live, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&restorePathSwap{live: live, staged: staged, backup: backup}).apply(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	resumed := &restorePathSwap{live: live, staged: staged, backup: backup}
	if err := resumed.apply(); err != nil {
		t.Fatal(err)
	}
	if err := resumed.rollback(); err != nil {
		t.Fatal(err)
	}
	assertRestoreFileContents(t, live, "old")
	assertRestoreFileContents(t, staged, "new")
}

func TestRestorePathSwapTracksPreviouslyAbsentLivePath(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live")
	staged := filepath.Join(root, "staged")
	backup := filepath.Join(root, "backup")
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&restorePathSwap{live: live, staged: staged, backup: backup}).apply(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	resumed := &restorePathSwap{live: live, staged: staged, backup: backup}
	if err := resumed.apply(); err != nil {
		t.Fatal(err)
	}
	if err := resumed.rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(live); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previously absent live path was restored: %v", err)
	}
	assertRestoreFileContents(t, staged, "new")
	if _, err := os.Lstat(backup + ".absent"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absence marker remains after rollback: %v", err)
	}
}

func TestRestoreTLSCutoverResumesAndRollsBackAppliedState(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, ".stage")
	backup := filepath.Join(root, "before-restore-job")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "live.pem"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "live.pem"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "live.pem"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	cutover := &restoreTLSCutover{root: root, stage: stage, backup: backup}
	if err := cutover.apply(); err != nil {
		t.Fatal(err)
	}
	if err := cutover.rollback(); err != nil {
		t.Fatal(err)
	}
	assertRestoreFileContents(t, filepath.Join(root, "live.pem"), "old")
	assertRestoreFileContents(t, filepath.Join(stage, "live.pem"), "new")
}

func TestRestoreTLSCutoverRetainsAmbiguousPartialState(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, ".stage")
	backup := filepath.Join(root, "before-restore-job")
	for _, directory := range []string{stage, backup} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stage, "pending.pem"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "live.pem"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (&restoreTLSCutover{root: root, stage: stage, backup: backup}).apply()
	if !errors.Is(err, errOnlineRestoreManualRollback) {
		t.Fatalf("partial TLS cutover error = %v", err)
	}
}

func TestPromoteRestoreClickHouseObservesRenameAfterLostResponse(t *testing.T) {
	clickHouse := &responseLostRestoreClickHouse{fakeRestoreClickHouse: &fakeRestoreClickHouse{databases: map[string]bool{
		"cdn_platform": true,
		"restore_temp": true,
	}}}
	job := &OnlineRestoreJob{TemporaryDatabase: "restore_temp", RollbackDatabase: "restore_old"}
	promoted, oldRenamed, err := promoteRestoreClickHouse(context.Background(), clickHouse, job)
	if err != nil {
		t.Fatal(err)
	}
	if !promoted || !oldRenamed {
		t.Fatalf("promotion state = promoted %v, old renamed %v", promoted, oldRenamed)
	}
	if exists, _ := clickHouse.DatabaseExists(context.Background(), "cdn_platform"); !exists {
		t.Fatal("restored database was not promoted")
	}
	if exists, _ := clickHouse.DatabaseExists(context.Background(), "restore_old"); !exists {
		t.Fatal("previous database was not retained")
	}
}

func TestOnlineRestoreMaintenanceLockIsExclusive(t *testing.T) {
	root := t.TempDir()
	if err := writeOnlineRestoreMaintenanceLock(root, "job-a"); err != nil {
		t.Fatal(err)
	}
	if err := writeOnlineRestoreMaintenanceLock(root, "job-b"); err == nil {
		t.Fatal("a second restore replaced the active maintenance marker")
	}
	contents, err := os.ReadFile(onlineRestoreMaintenancePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "job-a") || strings.Contains(string(contents), "job-b") {
		t.Fatalf("maintenance marker = %s", contents)
	}
	if err := removeOnlineRestoreMaintenanceLock(root, "job-b"); err == nil {
		t.Fatal("another job removed the active maintenance marker")
	}
	if err := removeOnlineRestoreMaintenanceLock(root, "job-a"); err != nil {
		t.Fatal(err)
	}
}

func assertRestoreFileContents(t *testing.T, path, want string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != want {
		t.Fatalf("%s = %q, want %q", path, contents, want)
	}
}

func writeRestoreTestArchive(path, root string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(archive, input)
		closeErr := input.Close()
		return errors.Join(copyErr, closeErr)
	})
	return errors.Join(walkErr, archive.Close(), compressed.Close(), file.Close())
}
