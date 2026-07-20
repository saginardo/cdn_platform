package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/integrations"
	"cdn-platform/internal/store"
)

type recordingBackupValidator struct {
	runtime BackupRuntime
	err     error
}

func (v *recordingBackupValidator) Validate(_ context.Context, runtime BackupRuntime) error {
	v.runtime = runtime
	return v.err
}

func TestSettingsAPIPreservesSecretsAndValidatesCloudflareBeforeSaving(t *testing.T) {
	const logo = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge", "203.0.113.82")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSite(domain.Site{Name: "zone-site", Domains: []string{"zone.example.test"}, Nodes: []string{node.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true}, "zone-a"); err != nil {
		t.Fatal(err)
	}
	key, _ := NewEncryptionKey()
	cipher, _ := NewCipher(key)
	environmentBackup := domain.BackupSettings{Repository: "s3:https://env.r2.example.test/env-backup", AccessKeyID: "env-access", Region: "auto", BackupTime: "03:25", RandomDelaySeconds: 1200}
	settings, err := NewSettingsManager(database, cipher, EnvironmentSettings{
		CloudflareAPIToken: "env-token", SMTPPassword: "env-password",
		Backup: environmentBackup, BackupAccessKey: "env-backup-secret", BackupPassword: "env-restic-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	cloudflareServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if request.URL.Path == "/user/tokens/verify" {
			status := "active"
			if token == "bad-token" {
				status = "disabled"
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "result": map[string]any{"status": status}})
			return
		}
		if request.URL.Path == "/zones/zone-a/dns_records" {
			if token != "good-token" && token != "env-token" {
				t.Fatalf("zone read used token %q", token)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "result": []integrations.DNSRecord{}, "result_info": map[string]any{"total_pages": 1}})
			return
		}
		t.Fatalf("unexpected Cloudflare path %s", request.URL.Path)
	}))
	defer cloudflareServer.Close()
	cloudflare := &integrations.CloudflareDNS{BaseURL: cloudflareServer.URL, Token: settings.CloudflareToken}
	backupValidator := &recordingBackupValidator{}
	server := &Server{Store: database, Cipher: cipher, Settings: settings, Cloudflare: cloudflare, BackupValidator: backupValidator}

	response := settingsRequest(t, server, http.MethodPut, "/api/settings/branding", map[string]any{"name": "", "subtitle": "控制面板"}, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid branding = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/branding", map[string]any{"name": "DustK CDN", "subtitle": "运营面板", "logo_data_url": logo}, true)
	if response.Code != http.StatusOK || settings.View().Branding.Name != "DustK CDN" || settings.View().Branding.Subtitle != "运营面板" || settings.View().Branding.LogoDataURL != logo {
		t.Fatalf("valid branding = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/branding", map[string]any{"name": "DustK CDN", "subtitle": "运营面板"}, true)
	if response.Code != http.StatusOK || settings.View().Branding.LogoDataURL != logo {
		t.Fatalf("legacy branding update did not preserve logo = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/branding", map[string]any{"name": "DustK CDN", "subtitle": "运营面板", "logo_data_url": "data:image/png;base64,invalid"}, true)
	if response.Code != http.StatusBadRequest || settings.View().Branding.LogoDataURL != logo {
		t.Fatalf("invalid branding logo = %d %s", response.Code, response.Body.String())
	}
	publicBranding := httptest.NewRecorder()
	server.Handler().ServeHTTP(publicBranding, httptest.NewRequest(http.MethodGet, "/api/branding", nil))
	if publicBranding.Code != http.StatusOK || !strings.Contains(publicBranding.Body.String(), logo) {
		t.Fatalf("public branding = %d %s", publicBranding.Code, publicBranding.Body.String())
	}

	response = settingsRequest(t, server, http.MethodPut, "/api/settings/dns", map[string]any{"default_ttl_seconds": 59}, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid TTL = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/dns", map[string]any{"default_ttl_seconds": 180}, true)
	if response.Code != http.StatusOK || settings.DNSDefaultTTL() != 180 {
		t.Fatalf("valid TTL = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/cache", map[string]any{"default_size_gb": 0}, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid cache default = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/cache", map[string]any{"default_size_gb": 4}, true)
	if response.Code != http.StatusOK || settings.CacheDefaultSizeGB() != 4 {
		t.Fatalf("valid cache default = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/cloudflare", map[string]any{"token": "bad-token"}, true)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("invalid token = %d %s", response.Code, response.Body.String())
	}
	if token, _ := settings.CloudflareToken(); token != "env-token" {
		t.Fatalf("invalid candidate replaced effective token: %q", token)
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/cloudflare", map[string]any{"token": "good-token"}, true)
	if response.Code != http.StatusOK {
		t.Fatalf("valid token = %d %s", response.Code, response.Body.String())
	}
	if token, _ := settings.CloudflareToken(); token != "good-token" {
		t.Fatalf("saved token = %q", token)
	}

	smtpPassword := "smtp-secret"
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/smtp", map[string]any{
		"enabled": true, "host": "smtp.example.test", "port": 465, "security": "tls", "username": "mailer", "password": smtpPassword,
		"from_address": "cdn@example.test", "recipients": []string{"ops@example.test"},
	}, true)
	if response.Code != http.StatusOK {
		t.Fatalf("SMTP save = %d %s", response.Code, response.Body.String())
	}
	backupSecret := "database-backup-secret"
	backupPassword := "database-restic-password"
	backupInput := map[string]any{
		"repository": "s3:https://account.r2.example.test/cdn-backup", "access_key_id": "database-access",
		"secret_access_key": backupSecret, "region": "auto", "restic_password": backupPassword,
		"backup_time": "04:20", "random_delay_seconds": 600,
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/backup", backupInput, true)
	if response.Code != http.StatusOK {
		t.Fatalf("backup save = %d %s", response.Code, response.Body.String())
	}
	delete(backupInput, "secret_access_key")
	delete(backupInput, "restic_password")
	response = settingsRequest(t, server, http.MethodPost, "/api/settings/backup/test", backupInput, true)
	if response.Code != http.StatusOK {
		t.Fatalf("backup test = %d %s", response.Code, response.Body.String())
	}
	if backupValidator.runtime.SecretAccessKey != backupSecret || backupValidator.runtime.ResticPassword != backupPassword {
		t.Fatalf("backup validator did not preserve stored secrets: %#v", backupValidator.runtime)
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/backup", map[string]any{
		"repository": "s3:https://missing-bucket.example.test", "access_key_id": "access", "region": "auto", "backup_time": "03:25", "random_delay_seconds": 0,
	}, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid backup repository = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodGet, "/api/settings", nil, false)
	if response.Code != http.StatusOK {
		t.Fatalf("settings GET = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"good-token", "env-token", smtpPassword, "env-password", backupSecret, backupPassword, "env-backup-secret", "env-restic-password"} {
		if strings.Contains(body, secret) {
			t.Fatalf("settings response leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"source":"database"`) || !strings.Contains(body, `"password_configured":true`) {
		t.Fatalf("settings response lacks non-secret status: %s", body)
	}
	if !strings.Contains(body, `"secret_access_key_configured":true`) || !strings.Contains(body, `"restic_password_configured":true`) {
		t.Fatalf("settings response lacks backup secret status: %s", body)
	}
	if !strings.Contains(body, `"branding":{"name":"DustK CDN","subtitle":"运营面板","logo_data_url":"data:image/png;base64,`) {
		t.Fatalf("settings response lacks branding: %s", body)
	}
	if !strings.Contains(body, `"cache":{"default_size_gb":4}`) {
		t.Fatalf("settings response lacks cache default: %s", body)
	}

	response = settingsRequest(t, server, http.MethodDelete, "/api/settings/cloudflare", nil, true)
	if response.Code != http.StatusOK {
		t.Fatalf("Cloudflare reset = %d %s", response.Code, response.Body.String())
	}
	if token, _ := settings.CloudflareToken(); token != "env-token" {
		t.Fatalf("reset token = %q", token)
	}
	response = settingsRequest(t, server, http.MethodDelete, "/api/settings/backup", nil, true)
	if response.Code != http.StatusOK {
		t.Fatalf("backup reset = %d %s", response.Code, response.Body.String())
	}
	if runtime := settings.BackupRuntime(); runtime.Settings != environmentBackup || runtime.SecretAccessKey != "env-backup-secret" || runtime.ResticPassword != "env-restic-password" {
		t.Fatalf("backup reset did not restore environment: %#v", runtime)
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/dns", map[string]any{"default_ttl_seconds": 120}, false)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d %s", response.Code, response.Body.String())
	}
}

func settingsRequest(t *testing.T, server *Server, method, path string, input any, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if csrf {
		request.Header.Set("X-CSRF-Token", "csrf-token")
	}
	request.AddCookie(&http.Cookie{Name: "cdn_session", Value: "session-token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
