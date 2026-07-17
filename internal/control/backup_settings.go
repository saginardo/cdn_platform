package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

type BackupRuntime struct {
	Settings        domain.BackupSettings
	SecretAccessKey string
	ResticPassword  string
}

type BackupRepositoryValidator interface {
	Validate(context.Context, BackupRuntime) error
}

type ResticBackupRepositoryValidator struct {
	Binary string
}

func (m *SettingsManager) BackupRuntime() BackupRuntime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return BackupRuntime{Settings: m.backup, SecretAccessKey: m.backupSecret, ResticPassword: m.backupPass}
}

func (m *SettingsManager) ResolveBackup(settings domain.BackupSettings, secretAccessKey, resticPassword *string) (BackupRuntime, error) {
	m.mu.RLock()
	currentSecret := m.backupSecret
	currentPassword := m.backupPass
	m.mu.RUnlock()
	return resolveBackupCandidate(settings, currentSecret, currentPassword, secretAccessKey, resticPassword)
}

func (m *SettingsManager) SaveBackup(settings domain.BackupSettings, secretAccessKey, resticPassword *string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.RLock()
	currentSecret := m.backupSecret
	currentPassword := m.backupPass
	wasOverride := m.backupDB
	m.mu.RUnlock()
	runtime, err := resolveBackupCandidate(settings, currentSecret, currentPassword, secretAccessKey, resticPassword)
	if err != nil {
		return err
	}
	replaceAccessKey := secretAccessKey != nil || !wasOverride
	replacePassword := resticPassword != nil || !wasOverride
	var encryptedAccessKey, encryptedPassword []byte
	if replaceAccessKey {
		encryptedAccessKey, err = m.Cipher.Encrypt([]byte(runtime.SecretAccessKey))
		if err != nil {
			return err
		}
	}
	if replacePassword {
		encryptedPassword, err = m.Cipher.Encrypt([]byte(runtime.ResticPassword))
		if err != nil {
			return err
		}
	}
	if err := m.Store.SaveBackupSettings(runtime.Settings, encryptedAccessKey, replaceAccessKey, encryptedPassword, replacePassword); err != nil {
		return err
	}
	m.mu.Lock()
	m.backup = runtime.Settings
	m.backupSecret = runtime.SecretAccessKey
	m.backupPass = runtime.ResticPassword
	m.backupDB = true
	m.mu.Unlock()
	return nil
}

func (m *SettingsManager) ClearBackup() error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	if err := m.Store.ClearBackupSettings(); err != nil {
		return err
	}
	m.mu.Lock()
	m.backup = m.env.Backup
	m.backupSecret = m.env.BackupAccessKey
	m.backupPass = m.env.BackupPassword
	m.backupDB = false
	m.mu.Unlock()
	return nil
}

func LoadBackupRuntime(database *store.Store, cipher *Cipher, environment BackupRuntime) (BackupRuntime, bool, error) {
	if database == nil || cipher == nil {
		return BackupRuntime{}, false, errors.New("backup settings store and cipher are required")
	}
	environment.Settings = domain.NormalizeBackupSettings(environment.Settings)
	persisted, err := database.BackupSettingsSnapshot()
	if err != nil {
		return BackupRuntime{}, false, err
	}
	if !persisted.Override {
		if err := domain.ValidateBackupSettings(environment.Settings, environment.SecretAccessKey, environment.ResticPassword); err != nil {
			return BackupRuntime{}, false, fmt.Errorf("environment backup settings: %w", err)
		}
		return environment, false, nil
	}
	runtime := BackupRuntime{Settings: domain.NormalizeBackupSettings(persisted.Settings)}
	runtime.SecretAccessKey, err = decryptBackupCiphertext(cipher, persisted.AccessKeyCiphertext, "S3 secret access key")
	if err != nil {
		return BackupRuntime{}, true, err
	}
	runtime.ResticPassword, err = decryptBackupCiphertext(cipher, persisted.PasswordCiphertext, "Restic repository password")
	if err != nil {
		return BackupRuntime{}, true, err
	}
	if err := domain.ValidateBackupSettings(runtime.Settings, runtime.SecretAccessKey, runtime.ResticPassword); err != nil {
		return BackupRuntime{}, true, fmt.Errorf("database backup settings: %w", err)
	}
	return runtime, true, nil
}

func resolveBackupCandidate(settings domain.BackupSettings, currentSecret, currentPassword string, secretAccessKey, resticPassword *string) (BackupRuntime, error) {
	runtime := BackupRuntime{
		Settings:        domain.NormalizeBackupSettings(settings),
		SecretAccessKey: currentSecret,
		ResticPassword:  currentPassword,
	}
	if secretAccessKey != nil {
		runtime.SecretAccessKey = *secretAccessKey
	}
	if resticPassword != nil {
		runtime.ResticPassword = *resticPassword
	}
	if err := domain.ValidateBackupSettings(runtime.Settings, runtime.SecretAccessKey, runtime.ResticPassword); err != nil {
		return BackupRuntime{}, err
	}
	return runtime, nil
}

func decryptBackupCiphertext(cipher *Cipher, ciphertext []byte, label string) (string, error) {
	if len(ciphertext) == 0 {
		return "", fmt.Errorf("stored backup override is missing the %s", label)
	}
	plaintext, err := cipher.Decrypt(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt %s: %w", label, err)
	}
	return string(plaintext), nil
}

func (v ResticBackupRepositoryValidator) Validate(ctx context.Context, runtime BackupRuntime) error {
	if err := domain.ValidateBackupSettings(runtime.Settings, runtime.SecretAccessKey, runtime.ResticPassword); err != nil {
		return err
	}
	binary := strings.TrimSpace(v.Binary)
	if binary == "" {
		binary = "restic"
	}
	cacheDir, err := os.MkdirTemp("", "cdn-restic-validation-*")
	if err != nil {
		return fmt.Errorf("create Restic validation cache: %w", err)
	}
	defer os.RemoveAll(cacheDir)
	passwordPath := filepath.Join(cacheDir, "repository-password")
	if err := os.WriteFile(passwordPath, []byte(runtime.ResticPassword), 0o600); err != nil {
		return fmt.Errorf("create Restic validation password file: %w", err)
	}
	command := exec.CommandContext(ctx, binary, "snapshots", "--latest", "1")
	command.Env = backupCommandEnvironment(runtime, cacheDir, passwordPath)
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	detail := string(output)
	for _, secret := range []string{runtime.SecretAccessKey, runtime.ResticPassword} {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[redacted]")
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 500 {
		detail = detail[:500]
	}
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("Restic repository validation failed: %s", detail)
}

func backupCommandEnvironment(runtime BackupRuntime, cacheDir, passwordPath string) []string {
	replaced := map[string]struct{}{
		"AWS_ACCESS_KEY_ID": {}, "AWS_SECRET_ACCESS_KEY": {}, "AWS_SESSION_TOKEN": {},
		"AWS_DEFAULT_REGION": {}, "AWS_REGION": {}, "RESTIC_REPOSITORY": {},
		"RESTIC_PASSWORD": {}, "RESTIC_PASSWORD_FILE": {}, "RESTIC_PASSWORD_COMMAND": {},
		"RESTIC_CACHE_DIR": {},
	}
	environment := make([]string, 0, len(os.Environ())+6)
	for _, value := range os.Environ() {
		name, _, found := strings.Cut(value, "=")
		if _, skip := replaced[name]; found && skip {
			continue
		}
		environment = append(environment, value)
	}
	return append(environment,
		"AWS_ACCESS_KEY_ID="+runtime.Settings.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY="+runtime.SecretAccessKey,
		"AWS_DEFAULT_REGION="+runtime.Settings.Region,
		"RESTIC_REPOSITORY="+runtime.Settings.Repository,
		"RESTIC_PASSWORD_FILE="+passwordPath,
		"RESTIC_CACHE_DIR="+cacheDir,
	)
}
