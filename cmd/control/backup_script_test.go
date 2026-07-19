package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRunScriptRetriesAndRecordsTerminalState(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	statusLog := filepath.Join(temporary, "status.log")
	attemptFile := filepath.Join(temporary, "attempt")
	writeExecutable(t, filepath.Join(temporary, "cdn-control"), `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STATUS_LOG"
`)
	backupCommand := filepath.Join(temporary, "backup")
	writeExecutable(t, backupCommand, `#!/usr/bin/env bash
attempt=0
if [[ -s "$ATTEMPT_FILE" ]]; then attempt=$(<"$ATTEMPT_FILE"); fi
attempt=$((attempt + 1))
printf '%s' "$attempt" >"$ATTEMPT_FILE"
if ((attempt < 3)); then
  echo "simulated failure $attempt" >&2
  exit 7
fi
`)
	command := exec.Command("bash", filepath.Join(repositoryRoot, "scripts", "compose-backup-run.sh"))
	command.Env = append(os.Environ(),
		"PATH="+temporary+":"+os.Getenv("PATH"),
		"STATUS_LOG="+statusLog,
		"ATTEMPT_FILE="+attemptFile,
		"BACKUP_COMMAND="+backupCommand,
		"BACKUP_STATUS_FILE="+filepath.Join(temporary, "backup.json"),
		"BACKUP_MAX_ATTEMPTS=3",
		"BACKUP_RETRY_DELAYS_SECONDS=0,0",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("backup wrapper: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(attemptFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "3" {
		t.Fatalf("attempts = %q", contents)
	}
	contents, err = os.ReadFile(statusLog)
	if err != nil {
		t.Fatal(err)
	}
	states := string(contents)
	for _, expected := range []string{" running 1 3 ", " retrying 1 3 ", " running 2 3 ", " retrying 2 3 ", " running 3 3 ", " succeeded 3 3 "} {
		if !strings.Contains(states, expected) {
			t.Fatalf("status log does not contain %q:\n%s", expected, states)
		}
	}
}

func TestBackupRunScriptRecordsFinalFailure(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	statusLog := filepath.Join(temporary, "status.log")
	writeExecutable(t, filepath.Join(temporary, "cdn-control"), `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STATUS_LOG"
`)
	backupCommand := filepath.Join(temporary, "backup")
	writeExecutable(t, backupCommand, "#!/usr/bin/env bash\necho 'permanent failure' >&2\nexit 9\n")
	command := exec.Command("bash", filepath.Join(repositoryRoot, "scripts", "compose-backup-run.sh"))
	command.Env = append(os.Environ(),
		"PATH="+temporary+":"+os.Getenv("PATH"),
		"STATUS_LOG="+statusLog,
		"BACKUP_COMMAND="+backupCommand,
		"BACKUP_STATUS_FILE="+filepath.Join(temporary, "backup.json"),
		"BACKUP_MAX_ATTEMPTS=2",
		"BACKUP_RETRY_DELAYS_SECONDS=0",
	)
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 9 {
		t.Fatalf("exit error = %v", err)
	}
	contents, err := os.ReadFile(statusLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), " failed 2 2 ") || !strings.Contains(string(contents), "permanent failure") {
		t.Fatalf("status log = %s", contents)
	}
}

func TestBackupRunScriptRetriesRetentionWithoutRepeatingSnapshot(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	statusLog := filepath.Join(temporary, "status.log")
	phaseLog := filepath.Join(temporary, "phases.log")
	retentionAttempts := filepath.Join(temporary, "retention-attempts")
	writeExecutable(t, filepath.Join(temporary, "cdn-control"), `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STATUS_LOG"
`)
	backupCommand := filepath.Join(temporary, "backup")
	writeExecutable(t, backupCommand, `#!/usr/bin/env bash
if [[ "${1-}" != "retention" ]]; then
  echo snapshot >>"$PHASE_LOG"
  echo "retention failed after snapshot" >&2
  exit 76
fi
echo retention >>"$PHASE_LOG"
attempt=0
if [[ -s "$RETENTION_ATTEMPTS" ]]; then attempt=$(<"$RETENTION_ATTEMPTS"); fi
attempt=$((attempt + 1))
printf '%s' "$attempt" >"$RETENTION_ATTEMPTS"
if ((attempt < 2)); then
  echo "retention still locked" >&2
  exit 11
fi
`)
	command := exec.Command("bash", filepath.Join(repositoryRoot, "scripts", "compose-backup-run.sh"))
	command.Env = append(os.Environ(),
		"PATH="+temporary+":"+os.Getenv("PATH"),
		"STATUS_LOG="+statusLog,
		"PHASE_LOG="+phaseLog,
		"RETENTION_ATTEMPTS="+retentionAttempts,
		"BACKUP_COMMAND="+backupCommand,
		"BACKUP_STATUS_FILE="+filepath.Join(temporary, "backup.json"),
		"BACKUP_MAX_ATTEMPTS=3",
		"BACKUP_RETRY_DELAYS_SECONDS=0,0",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("backup wrapper: %v\n%s", err, output)
	}
	phases, err := os.ReadFile(phaseLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(phases) != "snapshot\nretention\nretention\n" {
		t.Fatalf("backup phases = %q", phases)
	}
	statuses, err := os.ReadFile(statusLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(statuses), " succeeded 3 3 ") {
		t.Fatalf("status log = %s", statuses)
	}
}

func TestBackupRunScriptRecordsRestoreSkipWithoutRetry(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	statusLog := filepath.Join(temporary, "status.log")
	attemptFile := filepath.Join(temporary, "attempt")
	writeExecutable(t, filepath.Join(temporary, "cdn-control"), `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STATUS_LOG"
`)
	backupCommand := filepath.Join(temporary, "backup")
	writeExecutable(t, backupCommand, `#!/usr/bin/env bash
printf x >>"$ATTEMPT_FILE"
exit 75
`)
	command := exec.Command("bash", filepath.Join(repositoryRoot, "scripts", "compose-backup-run.sh"))
	command.Env = append(os.Environ(),
		"PATH="+temporary+":"+os.Getenv("PATH"),
		"STATUS_LOG="+statusLog,
		"ATTEMPT_FILE="+attemptFile,
		"ONLINE_RESTORE_ROOT="+filepath.Join(temporary, "restore"),
		"BACKUP_COMMAND="+backupCommand,
		"BACKUP_STATUS_FILE="+filepath.Join(temporary, "backup.json"),
		"BACKUP_MAX_ATTEMPTS=3",
		"BACKUP_RETRY_DELAYS_SECONDS=0,0",
	)
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 75 {
		t.Fatalf("exit error = %v", err)
	}
	attempts, err := os.ReadFile(attemptFile)
	if err != nil || string(attempts) != "x" {
		t.Fatalf("backup attempts = %q, %v", attempts, err)
	}
	statuses, err := os.ReadFile(statusLog)
	if err != nil || !strings.Contains(string(statuses), " skipped 1 3 ") || strings.Contains(string(statuses), " retrying ") {
		t.Fatalf("skip status = %q, %v", statuses, err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
