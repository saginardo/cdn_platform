package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResticCommandContextAllowsInterruptCleanup(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "restic")
	readyPath := filepath.Join(directory, "ready")
	cleanupPath := filepath.Join(directory, "cleaned")
	script := `#!/bin/sh
set -eu
trap 'touch "$CLEANUP_PATH"; exit 130' INT
touch "$READY_PATH"
while :; do :; done
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := resticCommandContext(ctx, binary)
	command.Env = append(os.Environ(), "READY_PATH="+readyPath, "CLEANUP_PATH="+cleanupPath)
	done := make(chan error, 1)
	go func() {
		_, err := command.CombinedOutput()
		done <- err
	}()
	waitForFile(t, readyPath, 2*time.Second)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled Restic command exited successfully")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled Restic command did not exit during its cleanup grace period")
	}
	if _, err := os.Stat(cleanupPath); err != nil {
		t.Fatalf("Restic command did not run interrupt cleanup: %v", err)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
