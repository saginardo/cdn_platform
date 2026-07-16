package control

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

func TestSettingsManagerUsesEncryptedDatabaseOverridesAndEnvironmentFallback(t *testing.T) {
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
	}
	manager, err := NewSettingsManager(database, cipher, environment)
	if err != nil {
		t.Fatal(err)
	}
	view := manager.View()
	if view.Cloudflare.Source != SettingsSourceEnvironment || view.SMTP.Source != SettingsSourceEnvironment || view.DNS.DefaultTTLSeconds != 60 {
		t.Fatalf("unexpected environment view: %#v", view)
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
	reloaded, err := NewSettingsManager(database, cipher, environment)
	if err != nil {
		t.Fatal(err)
	}
	view = reloaded.View()
	if view.Cloudflare.Source != SettingsSourceDatabase || view.SMTP.Source != SettingsSourceDatabase || view.SMTP.PasswordConfigured != true || view.DNS.DefaultTTLSeconds != 180 {
		t.Fatalf("unexpected reloaded view: %#v", view)
	}
	if token, _ := reloaded.CloudflareToken(); token != "database-token" {
		t.Fatalf("database token = %q", token)
	}
	loadedProfile, loadedPassword := reloaded.SMTPProfile()
	if loadedProfile.Host != profile.Host || loadedPassword != password {
		t.Fatalf("SMTP override = %#v, %q", loadedProfile, loadedPassword)
	}
	if err := reloaded.ClearCloudflareToken(); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.ClearSMTP(); err != nil {
		t.Fatal(err)
	}
	if token, _ := reloaded.CloudflareToken(); token != "env-token" {
		t.Fatalf("cleared token did not fall back to env: %q", token)
	}
	loadedProfile, loadedPassword = reloaded.SMTPProfile()
	if loadedProfile.Host != environment.SMTP.Host || loadedPassword != environment.SMTPPassword {
		t.Fatalf("cleared SMTP did not fall back to env: %#v, %q", loadedProfile, loadedPassword)
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
