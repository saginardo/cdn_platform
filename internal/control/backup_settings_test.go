package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cdn-platform/internal/domain"
)

func TestResticBackupRepositoryValidatorUsesIsolatedRuntimeEnvironment(t *testing.T) {
	directory := t.TempDir()
	outputPath := filepath.Join(directory, "environment")
	binary := filepath.Join(directory, "restic")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" "$AWS_DEFAULT_REGION" "$RESTIC_REPOSITORY" "${RESTIC_PASSWORD-}" "$RESTIC_CACHE_DIR" "${RESTIC_PASSWORD_FILE-}" "$(cat "$RESTIC_PASSWORD_FILE")" "$(stat -c '%a' "$RESTIC_PASSWORD_FILE")" > "$VALIDATOR_OUTPUT"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VALIDATOR_OUTPUT", outputPath)
	t.Setenv("AWS_ACCESS_KEY_ID", "stale-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "stale-secret")
	t.Setenv("RESTIC_PASSWORD", "stale-password")
	t.Setenv("RESTIC_PASSWORD_FILE", "/stale/password-file")
	runtime := validBackupRuntime()
	validator := ResticBackupRepositoryValidator{Binary: binary}
	if err := validator.Validate(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("validator environment lines = %q", lines)
	}
	if lines[0] != "snapshots --latest 1" || lines[1] != runtime.Settings.AccessKeyID || lines[2] != runtime.SecretAccessKey || lines[3] != runtime.Settings.Region || lines[4] != runtime.Settings.Repository || lines[5] != "" || lines[6] == "" || lines[7] == "" || lines[8] != runtime.ResticPassword || lines[9] != "600" {
		t.Fatalf("validator environment = %q", lines)
	}
	if strings.Contains(string(contents), "stale-") {
		t.Fatalf("validator inherited stale backup credentials: %s", contents)
	}
}

func TestResticBackupRepositoryValidatorRedactsSecretsFromErrors(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "restic")
	runtime := validBackupRuntime()
	script := "#!/bin/sh\necho 'failed with " + runtime.SecretAccessKey + " and " + runtime.ResticPassword + "' >&2\nexit 1\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	err := (ResticBackupRepositoryValidator{Binary: binary}).Validate(context.Background(), runtime)
	if err == nil {
		t.Fatal("validator accepted a failed Restic command")
	}
	if strings.Contains(err.Error(), runtime.SecretAccessKey) || strings.Contains(err.Error(), runtime.ResticPassword) {
		t.Fatalf("validator error leaked a secret: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("validator error did not retain a useful redaction marker: %v", err)
	}
}

func validBackupRuntime() BackupRuntime {
	return BackupRuntime{
		Settings: domain.BackupSettings{
			Repository:         "s3:https://account.r2.example.test/cdn-backup",
			AccessKeyID:        "database-access-key",
			Region:             "auto",
			BackupTime:         "03:25",
			RandomDelaySeconds: 1200,
		},
		SecretAccessKey: "database-secret-key",
		ResticPassword:  "database-restic-password",
	}
}
