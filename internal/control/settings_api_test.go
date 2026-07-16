package control

import (
	"bytes"
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

func TestSettingsAPIPreservesSecretsAndValidatesCloudflareBeforeSaving(t *testing.T) {
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
	settings, err := NewSettingsManager(database, cipher, EnvironmentSettings{CloudflareAPIToken: "env-token", SMTPPassword: "env-password"})
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
	server := &Server{Store: database, Cipher: cipher, Settings: settings, Cloudflare: cloudflare}

	response := settingsRequest(t, server, http.MethodPut, "/api/settings/dns", map[string]any{"default_ttl_seconds": 59}, true)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid TTL = %d %s", response.Code, response.Body.String())
	}
	response = settingsRequest(t, server, http.MethodPut, "/api/settings/dns", map[string]any{"default_ttl_seconds": 180}, true)
	if response.Code != http.StatusOK || settings.DNSDefaultTTL() != 180 {
		t.Fatalf("valid TTL = %d %s", response.Code, response.Body.String())
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
	response = settingsRequest(t, server, http.MethodGet, "/api/settings", nil, false)
	if response.Code != http.StatusOK {
		t.Fatalf("settings GET = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"good-token", "env-token", smtpPassword, "env-password"} {
		if strings.Contains(body, secret) {
			t.Fatalf("settings response leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"source":"database"`) || !strings.Contains(body, `"password_configured":true`) {
		t.Fatalf("settings response lacks non-secret status: %s", body)
	}

	response = settingsRequest(t, server, http.MethodDelete, "/api/settings/cloudflare", nil, true)
	if response.Code != http.StatusOK {
		t.Fatalf("Cloudflare reset = %d %s", response.Code, response.Body.String())
	}
	if token, _ := settings.CloudflareToken(); token != "env-token" {
		t.Fatalf("reset token = %q", token)
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
