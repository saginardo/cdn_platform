package control

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

func TestSettingsManagerUsesEncryptedDatabaseOverridesAndEnvironmentFallback(t *testing.T) {
	const logo = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, err := NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	environment := EnvironmentSettings{
		CloudflareAPIToken: "env-token",
		SMTP:               SMTPProfile{Enabled: true, Host: "env.smtp.example.test", Port: 587, Username: "env-user", FromAddress: "cdn@example.test", Recipients: []string{"ops@example.test"}, Security: integrations.SMTPSecurityStartTLS},
		SMTPPassword:       "env-password",
		Backup: domain.BackupSettings{
			Repository: "s3:https://env.r2.example.test/env-backup", AccessKeyID: "env-access", Region: "auto", BackupTime: "03:25", RandomDelaySeconds: 1200,
		},
		BackupAccessKey: "env-backup-secret",
		BackupPassword:  "env-restic-password",
	}
	manager, err := NewSettingsManager(database, cipher, environment)
	if err != nil {
		t.Fatal(err)
	}
	view := manager.View()
	if view.Cloudflare.Source != SettingsSourceEnvironment || view.SMTP.Source != SettingsSourceEnvironment || view.Backup.Source != SettingsSourceEnvironment || view.DNS.DefaultTTLSeconds != 60 || view.Cache.DefaultSizeGB != 1 {
		t.Fatalf("unexpected environment view: %#v", view)
	}
	if view.Branding != domain.DefaultBrandingSettings() {
		t.Fatalf("unexpected branding defaults: %#v", view.Branding)
	}
	branding := domain.BrandingSettings{Name: "DustK CDN", Subtitle: "运营面板", LogoDataURL: logo}
	if err := manager.SaveBranding(branding); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveBranding(domain.BrandingSettings{Name: "\n"}); err == nil {
		t.Fatal("accepted invalid branding")
	}
	if token, _ := manager.CloudflareToken(); token != "env-token" {
		t.Fatalf("environment token = %q", token)
	}
	if err := manager.SaveCloudflareToken("database-token"); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := database.Secret(store.SecretCloudflareAPIToken)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("database-token")) {
		t.Fatal("Cloudflare token stored as plaintext")
	}
	password := "database-password"
	profile := SMTPProfile{Enabled: true, Host: "db.smtp.example.test", Port: 465, Username: "db-user", FromAddress: "cdn@example.test", Recipients: []string{"alerts@example.test"}, Security: integrations.SMTPSecurityTLS}
	if err := manager.SaveSMTP(profile, &password); err != nil {
		t.Fatal(err)
	}
	smtpCiphertext, err := database.Secret(store.SecretSMTPPassword)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(smtpCiphertext, []byte(password)) {
		t.Fatal("SMTP password stored as plaintext")
	}
	if err := manager.SaveDNSDefaultTTL(180); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveCacheDefaultSizeGB(6); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveCacheDefaultSizeGB(1025); err == nil {
		t.Fatal("accepted invalid cache default")
	}
	backupSecret := "database-backup-secret"
	backupPassword := "database-restic-password"
	backup := domain.BackupSettings{Repository: "s3:https://db.r2.example.test/db-backup", AccessKeyID: "db-access", Region: "auto", BackupTime: "04:15", RandomDelaySeconds: 300}
	if err := manager.SaveBackup(backup, &backupSecret, &backupPassword); err != nil {
		t.Fatal(err)
	}
	backup.BackupTime = "04:30"
	if err := manager.SaveBackup(backup, nil, nil); err != nil {
		t.Fatalf("update backup while preserving secrets: %v", err)
	}
	for name, plaintext := range map[string]string{store.SecretBackupAccessKey: backupSecret, store.SecretBackupPassword: backupPassword} {
		ciphertext, err := database.Secret(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(ciphertext, []byte(plaintext)) {
			t.Fatalf("backup secret %s stored as plaintext", name)
		}
	}
	reloaded, err := NewSettingsManager(database, cipher, environment)
	if err != nil {
		t.Fatal(err)
	}
	view = reloaded.View()
	if view.Cloudflare.Source != SettingsSourceDatabase || view.SMTP.Source != SettingsSourceDatabase || view.SMTP.PasswordConfigured != true || view.Backup.Source != SettingsSourceDatabase || !view.Backup.Configured || view.DNS.DefaultTTLSeconds != 180 || view.Cache.DefaultSizeGB != 6 {
		t.Fatalf("unexpected reloaded view: %#v", view)
	}
	if view.Branding != branding {
		t.Fatalf("reloaded branding = %#v", view.Branding)
	}
	if token, _ := reloaded.CloudflareToken(); token != "database-token" {
		t.Fatalf("database token = %q", token)
	}
	loadedProfile, loadedPassword := reloaded.SMTPProfile()
	if loadedProfile.Host != profile.Host || loadedPassword != password {
		t.Fatalf("SMTP override = %#v, %q", loadedProfile, loadedPassword)
	}
	loadedBackup := reloaded.BackupRuntime()
	if loadedBackup.Settings != backup || loadedBackup.SecretAccessKey != backupSecret || loadedBackup.ResticPassword != backupPassword {
		t.Fatalf("backup override = %#v", loadedBackup)
	}
	if err := reloaded.ClearCloudflareToken(); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.ClearSMTP(); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.ClearBackup(); err != nil {
		t.Fatal(err)
	}
	if token, _ := reloaded.CloudflareToken(); token != "env-token" {
		t.Fatalf("cleared token did not fall back to env: %q", token)
	}
	loadedProfile, loadedPassword = reloaded.SMTPProfile()
	if loadedProfile.Host != environment.SMTP.Host || loadedPassword != environment.SMTPPassword {
		t.Fatalf("cleared SMTP did not fall back to env: %#v, %q", loadedProfile, loadedPassword)
	}
	loadedBackup = reloaded.BackupRuntime()
	if loadedBackup.Settings != environment.Backup || loadedBackup.SecretAccessKey != environment.BackupAccessKey || loadedBackup.ResticPassword != environment.BackupPassword {
		t.Fatalf("cleared backup did not fall back to env: %#v", loadedBackup)
	}
	if _, err := database.Secret(store.SecretCloudflareAPIToken); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Cloudflare override remains: %v", err)
	}
}

func TestSettingsManagerValidatesSMTPProfile(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, _ := NewEncryptionKey()
	cipher, _ := NewCipher(key)
	manager, err := NewSettingsManager(database, cipher, EnvironmentSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if manager.View().SMTP.Recipients == nil {
		t.Fatal("settings view returned null SMTP recipients")
	}
	profile := SMTPProfile{Enabled: true, Host: "smtp.example.test", Port: 587, Username: "user", FromAddress: "cdn@example.test", Recipients: []string{"ops@example.test"}, Security: integrations.SMTPSecurityStartTLS}
	if err := manager.SaveSMTP(profile, nil); err == nil {
		t.Fatal("accepted authenticated SMTP without a password")
	}
	password := "secret"
	profile.Security = "none"
	if err := manager.SaveSMTP(profile, &password); err == nil {
		t.Fatal("accepted plaintext SMTP")
	}
	profile.Security = integrations.SMTPSecurityStartTLS
	profile.Recipients = []string{"bad\naddress@example.test"}
	if err := manager.SaveSMTP(profile, &password); err == nil {
		t.Fatal("accepted SMTP header injection")
	}
}

func TestSettingsManagerFiltersCategoriesAndPersistsNotificationCooldown(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	key, _ := NewEncryptionKey()
	cipher, _ := NewCipher(key)
	environment := EnvironmentSettings{SMTP: SMTPProfile{
		Enabled: true, Host: "smtp.example.test", Port: 465,
		FromAddress: "cdn@example.test", Recipients: []string{"ops@example.test"},
		NotificationCategories: []string{"monitoring"}, Security: integrations.SMTPSecurityTLS,
	}}
	manager, err := NewSettingsManager(database, cipher, environment)
	if err != nil {
		t.Fatal(err)
	}
	delivered := make([]integrations.Notification, 0)
	manager.notificationSender = func(_ context.Context, _ SMTPProfile, _ string, notification integrations.Notification) error {
		delivered = append(delivered, notification)
		return nil
	}
	availability := integrations.Notification{Category: integrations.NotificationCategoryAvailability, Subject: "availability"}
	if err := manager.NotifyNotification(context.Background(), availability); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 0 {
		t.Fatalf("disabled category delivered notifications: %#v", delivered)
	}
	const deliveryKey = "monitoring:node:test"
	anomaly := integrations.Notification{
		Category: integrations.NotificationCategoryMonitoring, Subject: "anomaly", Key: deliveryKey,
		Cooldown: 5 * time.Minute, SuppressUntilResolved: true,
	}
	if err := manager.NotifyNotification(context.Background(), anomaly); err != nil {
		t.Fatal(err)
	}
	if err := manager.NotifyNotification(context.Background(), anomaly); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatalf("same incident delivered %d notifications", len(delivered))
	}
	reloaded, err := NewSettingsManager(database, cipher, environment)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.notificationSender = func(_ context.Context, _ SMTPProfile, _ string, notification integrations.Notification) error {
		delivered = append(delivered, notification)
		return nil
	}
	if err := reloaded.NotifyNotification(context.Background(), anomaly); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatal("cooldown did not survive settings manager restart")
	}
	recovery := integrations.Notification{
		Category: integrations.NotificationCategoryMonitoring, Subject: "recovered", Key: deliveryKey,
		Resolved: true, NotifyOnResolve: true,
	}
	if err := reloaded.NotifyNotification(context.Background(), recovery); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || !delivered[1].Resolved {
		t.Fatalf("recovery notifications = %#v", delivered)
	}
	if err := reloaded.NotifyNotification(context.Background(), anomaly); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 {
		t.Fatal("resolved incident bypassed the five-minute cooldown")
	}
	if err := database.MarkNotificationDelivered(deliveryKey, false, time.Now().UTC().Add(-6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.NotifyNotification(context.Background(), anomaly); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 3 {
		t.Fatal("notification remained suppressed after cooldown elapsed")
	}
}
