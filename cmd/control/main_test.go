package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cdn-platform/internal/control"
	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

func TestWriteCloudflareCredentialsUsesDatabaseOverride(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CONTROL_DATA_DIR", dataDir)
	t.Setenv("CLOUDFLARE_API_TOKEN", "environment-token")
	key, err := control.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_ENCRYPTION_KEY", key)
	cipher, err := control.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(dataDir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ciphertext, err := cipher.Encrypt([]byte("database-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetSecret(store.SecretCloudflareAPIToken, ciphertext); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cloudflare.ini")
	writeCloudflareCredentials(path)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "dns_cloudflare_api_token = database-token\n" || strings.Contains(string(contents), "environment-token") {
		t.Fatalf("credentials = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %o", info.Mode().Perm())
	}
	rotated, err := cipher.Encrypt([]byte("rotated-database-token"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetSecret(store.SecretCloudflareAPIToken, rotated); err != nil {
		t.Fatal(err)
	}
	writeCloudflareCredentials(path)
	contents, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "dns_cloudflare_api_token = rotated-database-token\n" {
		t.Fatalf("rotated credentials = %q", contents)
	}
}

func TestWriteCloudflareCredentialsFallsBackToEnvironment(t *testing.T) {
	t.Setenv("CONTROL_DATA_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_API_TOKEN", "environment-token")
	path := filepath.Join(t.TempDir(), "cloudflare.ini")
	writeCloudflareCredentials(path)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "dns_cloudflare_api_token = environment-token\n" {
		t.Fatalf("credentials = %q", contents)
	}
}

func TestSettingsFromEnvUsesTLSDefaultsAndRejectsUnsafeModes(t *testing.T) {
	t.Setenv("SMTP_TO", "ops@example.test")
	t.Setenv("SMTP_SECURITY", "tls")
	t.Setenv("SMTP_PORT", "")
	settings, err := settingsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !settings.SMTP.Enabled || settings.SMTP.Port != 465 || settings.SMTP.Security != "tls" {
		t.Fatalf("implicit TLS environment = %#v", settings.SMTP)
	}
	t.Setenv("SMTP_SECURITY", "none")
	if _, err := settingsFromEnv(); err == nil {
		t.Fatal("accepted plaintext SMTP environment")
	}
}

func TestPublishSiteCommandPublishesOnlySelectedSite(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CONTROL_DATA_DIR", dataDir)
	key, err := control.NewEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_ENCRYPTION_KEY", key)
	database, err := store.Open(filepath.Join(dataDir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge", "203.0.113.50")
	if err != nil {
		t.Fatal(err)
	}
	target, err := database.CreateSite(controlTestSite("target", "target.example.test", node.ID), "zone")
	if err != nil {
		t.Fatal(err)
	}
	other, err := database.CreateSite(controlTestSite("other", "other.example.test", node.ID), "zone")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	publishSite(target.ID)

	database, err = store.Open(filepath.Join(dataDir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.SitePublication(target.ID); err != nil {
		t.Fatalf("target site was not published: %v", err)
	}
	if _, err := database.SitePublication(other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unrelated site publication = %v", err)
	}
}

func controlTestSite(name, domainName, nodeID string) domain.Site {
	return domain.Site{
		Name: name, Domains: []string{domainName}, Nodes: []string{nodeID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled:       false,
	}
}

func TestComposeCertbotRefreshesCredentialsForEveryCertificateOperation(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "scripts", "compose-certbot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(contents)
	if !strings.Contains(script, `cdn-control cloudflare-credentials "$credentials"`) {
		t.Fatal("compose certbot does not load runtime Cloudflare credentials")
	}
	if count := strings.Count(script, "  write_credentials\n"); count != 2 {
		t.Fatalf("credential refresh count = %d, want issue and renew", count)
	}
	if strings.Contains(script, "dns_cloudflare_api_token = %s") {
		t.Fatal("compose certbot still writes the environment token directly")
	}
}
