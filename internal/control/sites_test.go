package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

func TestSiteClientMaxBodySizeAPI(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	defaultSite := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
		"name": "default", "zone_id": "zone", "domains": []string{"default.example.test"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "enabled": true,
	})
	if defaultSite.ClientMaxBodySizeMB != domain.DefaultClientMaxBodySizeMB {
		t.Fatalf("omitted client max body size = %d", defaultSite.ClientMaxBodySizeMB)
	}

	var largest domain.Site
	for _, value := range []int{128, 256, 512, 1024} {
		created := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
			"name": fmt.Sprintf("body-%d", value), "zone_id": "zone", "domains": []string{fmt.Sprintf("body-%d.example.test", value)}, "node_ids": []string{node.ID},
			"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "client_max_body_size_mb": value, "enabled": true,
		})
		if created.ClientMaxBodySizeMB != value {
			t.Fatalf("created client max body size = %d, want %d", created.ClientMaxBodySizeMB, value)
		}
		if value == 1024 {
			largest = created
		}
	}

	updated := requestSite(t, server, http.MethodPut, "/api/sites/"+largest.ID, map[string]any{
		"name": largest.Name, "zone_id": largest.ZoneID, "domains": largest.Domains, "node_ids": largest.Nodes,
		"primary_origin": largest.PrimaryOrigin, "enabled": largest.Enabled,
	})
	if updated.ClientMaxBodySizeMB != 1024 {
		t.Fatalf("omitted update did not preserve client max body size: %#v", updated)
	}

	for _, value := range []int{0, 127, 129, 1025} {
		response := requestSiteResponse(t, server, http.MethodPost, "/api/sites", map[string]any{
			"name": "invalid", "zone_id": "zone", "domains": []string{"invalid.example.test"}, "node_ids": []string{node.ID},
			"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "client_max_body_size_mb": value, "enabled": true,
		})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("client max body size %d = %d %s", value, response.Code, response.Body.String())
		}
	}

	before, _, err := database.GetSite(largest.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := requestSiteResponse(t, server, http.MethodPut, "/api/sites/"+largest.ID, map[string]any{
		"name": largest.Name, "zone_id": largest.ZoneID, "domains": largest.Domains, "node_ids": largest.Nodes,
		"primary_origin": largest.PrimaryOrigin, "client_max_body_size_mb": 129, "enabled": largest.Enabled,
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid update = %d %s", response.Code, response.Body.String())
	}
	after, _, err := database.GetSite(largest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ClientMaxBodySizeMB != before.ClientMaxBodySizeMB || after.ConfigVersion != before.ConfigVersion {
		t.Fatalf("invalid update changed site: before=%#v after=%#v", before, after)
	}
}

func TestSiteDNSTTLAPIHandlesOverrideInheritanceAndOmission(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-ttl", "203.0.113.81")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	base := map[string]any{
		"name": "ttl", "zone_id": "zone", "domains": []string{"ttl.example.test"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "enabled": true,
	}
	created := requestSite(t, server, http.MethodPost, "/api/sites", base)
	if created.DNSTTLSeconds != nil {
		t.Fatalf("omitted TTL did not inherit: %#v", created.DNSTTLSeconds)
	}
	base["dns_ttl_seconds"] = 180
	updated := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, base)
	if updated.DNSTTLSeconds == nil || *updated.DNSTTLSeconds != 180 {
		t.Fatalf("TTL override = %#v", updated.DNSTTLSeconds)
	}
	delete(base, "dns_ttl_seconds")
	preserved := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, base)
	if preserved.DNSTTLSeconds == nil || *preserved.DNSTTLSeconds != 180 {
		t.Fatalf("omitted update did not preserve TTL: %#v", preserved.DNSTTLSeconds)
	}
	base["dns_ttl_seconds"] = nil
	inherited := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, base)
	if inherited.DNSTTLSeconds != nil {
		t.Fatalf("explicit null did not restore inheritance: %#v", inherited.DNSTTLSeconds)
	}
	for _, value := range []int{59, 301} {
		base["dns_ttl_seconds"] = value
		response := requestSiteResponse(t, server, http.MethodPut, "/api/sites/"+created.ID, base)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("TTL %d = %d %s", value, response.Code, response.Body.String())
		}
	}
}

func TestOriginAllowlistIncludesPublishedAndDraftAssignments(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	oldNode, err := database.CreateNode("edge-old", "203.0.113.72")
	if err != nil {
		t.Fatal(err)
	}
	newNode, err := database.CreateNode("edge-new", "203.0.113.73")
	if err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "allowlist", Domains: []string{"allowlist.example.test"}, Nodes: []string{oldNode.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.MarkSitePublished(site.ID); err != nil {
		t.Fatal(err)
	}
	draft, zoneID, err := database.GetSite(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Nodes = []string{newNode.ID}
	if _, err := database.UpdateSite(draft, zoneID); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sites/"+site.ID+"/origin-allowlist", nil)
	request.SetPathValue("id", site.ID)
	response := httptest.NewRecorder()
	(&Server{Store: database}).originAllowlist(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("allowlist response = %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		CIDRs []string `json:"ipv4_cidrs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.CIDRs) != 2 || payload.CIDRs[0] != newNode.PublicIPv4+"/32" || payload.CIDRs[1] != oldNode.PublicIPv4+"/32" {
		t.Fatalf("allowlist omitted a published or draft node: %#v", payload.CIDRs)
	}
}

func TestSiteReadWriteTimeoutAPI(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	defaultSite := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
		"name": "default-timeout", "zone_id": "zone", "domains": []string{"default-timeout.example.test"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "stream_paths": []string{"/legacy"}, "enabled": true,
	})
	if defaultSite.ReadWriteTimeoutSeconds != domain.DefaultReadWriteTimeoutSeconds {
		t.Fatalf("omitted read/write timeout = %d", defaultSite.ReadWriteTimeoutSeconds)
	}
	if defaultSite.StreamPaths == nil || len(defaultSite.StreamPaths) != 0 {
		t.Fatalf("legacy stream paths were not retired: %#v", defaultSite.StreamPaths)
	}

	var longest domain.Site
	for _, value := range []int{360, 900, 1800, 3600} {
		created := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
			"name": fmt.Sprintf("timeout-%d", value), "zone_id": "zone", "domains": []string{fmt.Sprintf("timeout-%d.example.test", value)}, "node_ids": []string{node.ID},
			"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "read_write_timeout_seconds": value, "enabled": true,
		})
		if created.ReadWriteTimeoutSeconds != value {
			t.Fatalf("created read/write timeout = %d, want %d", created.ReadWriteTimeoutSeconds, value)
		}
		if value == 3600 {
			longest = created
		}
	}

	updated := requestSite(t, server, http.MethodPut, "/api/sites/"+longest.ID, map[string]any{
		"name": longest.Name, "zone_id": longest.ZoneID, "domains": longest.Domains, "node_ids": longest.Nodes,
		"primary_origin": longest.PrimaryOrigin, "stream_paths": []string{"/legacy"}, "enabled": longest.Enabled,
	})
	if updated.ReadWriteTimeoutSeconds != 3600 {
		t.Fatalf("omitted update did not preserve read/write timeout: %#v", updated)
	}
	if updated.StreamPaths == nil || len(updated.StreamPaths) != 0 {
		t.Fatalf("legacy stream paths were not ignored on update: %#v", updated.StreamPaths)
	}

	for _, value := range []int{0, 359, 361, 7200} {
		response := requestSiteResponse(t, server, http.MethodPost, "/api/sites", map[string]any{
			"name": "invalid-timeout", "zone_id": "zone", "domains": []string{"invalid-timeout.example.test"}, "node_ids": []string{node.ID},
			"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "read_write_timeout_seconds": value, "enabled": true,
		})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("read/write timeout %d = %d %s", value, response.Code, response.Body.String())
		}
	}

	before, _, err := database.GetSite(longest.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := requestSiteResponse(t, server, http.MethodPut, "/api/sites/"+longest.ID, map[string]any{
		"name": longest.Name, "zone_id": longest.ZoneID, "domains": longest.Domains, "node_ids": longest.Nodes,
		"primary_origin": longest.PrimaryOrigin, "read_write_timeout_seconds": 901, "enabled": longest.Enabled,
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid timeout update = %d %s", response.Code, response.Body.String())
	}
	after, _, err := database.GetSite(longest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ReadWriteTimeoutSeconds != before.ReadWriteTimeoutSeconds || after.ConfigVersion != before.ConfigVersion {
		t.Fatalf("invalid timeout update changed site: before=%#v after=%#v", before, after)
	}
}

func TestSiteOriginTLSServerNameAPI(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}

	created := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
		"name": "ip-origin", "zone_id": "zone", "domains": []string{"lax.dustvm.de"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://203.0.113.20:443", "host_header": "lax.dustvm.de", "tls_server_name": "LAX.DUSTVM.DE", "enabled": true},
		"backup_origin":  map[string]any{"url": "https://203.0.113.21:443", "host_header": "backup.dustvm.de", "tls_server_name": "backup.dustvm.de", "enabled": true},
		"enabled":        true,
	})
	if created.PrimaryOrigin.TLSServerName != "lax.dustvm.de" || created.BackupOrigin == nil || created.BackupOrigin.TLSServerName != "backup.dustvm.de" {
		t.Fatalf("unexpected TLS server names: %#v", created)
	}
	loaded, _, err := database.GetSite(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PrimaryOrigin.TLSServerName != "lax.dustvm.de" || loaded.BackupOrigin == nil || loaded.BackupOrigin.TLSServerName != "backup.dustvm.de" {
		t.Fatalf("stored TLS server names were not preserved: %#v", loaded)
	}

	updated := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, map[string]any{
		"name": created.Name, "zone_id": created.ZoneID, "domains": created.Domains, "node_ids": created.Nodes,
		"primary_origin": map[string]any{"url": created.PrimaryOrigin.URL, "host_header": created.PrimaryOrigin.HostHeader, "enabled": true},
		"backup_origin":  map[string]any{"url": created.BackupOrigin.URL, "host_header": created.BackupOrigin.HostHeader, "enabled": true}, "enabled": created.Enabled,
	})
	if updated.PrimaryOrigin.TLSServerName != "lax.dustvm.de" || updated.BackupOrigin == nil || updated.BackupOrigin.TLSServerName != "backup.dustvm.de" {
		t.Fatalf("omitted TLS server names did not preserve the existing values: %#v", updated)
	}
	cleared := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, map[string]any{
		"name": updated.Name, "zone_id": updated.ZoneID, "domains": updated.Domains, "node_ids": updated.Nodes,
		"primary_origin": map[string]any{"url": updated.PrimaryOrigin.URL, "host_header": updated.PrimaryOrigin.HostHeader, "tls_server_name": "", "enabled": true},
		"backup_origin":  updated.BackupOrigin, "enabled": updated.Enabled,
	})
	if cleared.PrimaryOrigin.TLSServerName != "" {
		t.Fatalf("explicitly empty TLS server name was not cleared: %#v", cleared.PrimaryOrigin)
	}
	movedBackup := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, map[string]any{
		"name": cleared.Name, "zone_id": cleared.ZoneID, "domains": cleared.Domains, "node_ids": cleared.Nodes,
		"primary_origin": cleared.PrimaryOrigin,
		"backup_origin":  map[string]any{"url": "https://203.0.113.22:443", "host_header": cleared.BackupOrigin.HostHeader, "enabled": true}, "enabled": cleared.Enabled,
	})
	if movedBackup.BackupOrigin == nil || movedBackup.BackupOrigin.TLSServerName != "" {
		t.Fatalf("omitted TLS server name was carried to a different backup URL: %#v", movedBackup.BackupOrigin)
	}

	defaultResponse := requestSiteResponse(t, server, http.MethodPost, "/api/sites", map[string]any{
		"name": "default-sni", "zone_id": "zone", "domains": []string{"default-sni.example.test"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "enabled": true,
	})
	if defaultResponse.Code != http.StatusCreated {
		t.Fatalf("default TLS server name create = %d %s", defaultResponse.Code, defaultResponse.Body.String())
	}
	if strings.Contains(defaultResponse.Body.String(), `"tls_server_name"`) {
		t.Fatalf("empty TLS server name should be omitted from the API response: %s", defaultResponse.Body.String())
	}

	for name, origin := range map[string]map[string]any{
		"plain HTTP": {"url": "http://203.0.113.20:80", "tls_server_name": "lax.dustvm.de", "enabled": true},
		"IP name":    {"url": "https://203.0.113.20:443", "tls_server_name": "203.0.113.20", "enabled": true},
		"wildcard":   {"url": "https://203.0.113.20:443", "tls_server_name": "*.dustvm.de", "enabled": true},
	} {
		t.Run(name, func(t *testing.T) {
			response := requestSiteResponse(t, server, http.MethodPost, "/api/sites", map[string]any{
				"name": "invalid-" + strings.ReplaceAll(name, " ", "-"), "zone_id": "zone", "domains": []string{"invalid-" + strings.ReplaceAll(name, " ", "-") + ".example.test"}, "node_ids": []string{node.ID},
				"primary_origin": origin, "enabled": true,
			})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid TLS server name = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSitePassthroughAPICompatibilityAndCacheInvalidation(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.CreateInitialAdmin("hash", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSession("admin", "session-token", "csrf-token", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	node, err := database.CreateNode("edge-1", "203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	defaultSite := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
		"name": "cached", "zone_id": "zone", "domains": []string{"cached.example.test"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "enabled": true,
	})
	if defaultSite.Passthrough {
		t.Fatalf("omitted passthrough should default to cached mode: %#v", defaultSite)
	}
	invalidBody := []byte(`{"name":"grpc","zone_id":"zone","domains":["grpc.example.test"],"node_ids":["` + node.ID + `"],"primary_origin":{"url":"grpcs://origin.example.test","enabled":true},"passthrough":true,"enabled":true}`)
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/sites", bytes.NewReader(invalidBody))
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalidRequest.Header.Set("X-CSRF-Token", "csrf-token")
	invalidRequest.AddCookie(&http.Cookie{Name: "cdn_session", Value: "session-token"})
	invalidResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("gRPC passthrough = %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}

	created := requestSite(t, server, http.MethodPost, "/api/sites", map[string]any{
		"name": "stream", "zone_id": "zone", "domains": []string{"stream.example.test"}, "node_ids": []string{node.ID},
		"primary_origin": map[string]any{"url": "https://origin.example.test", "enabled": true}, "passthrough": true, "enabled": true,
	})
	if !created.Passthrough || created.CacheGeneration != 1 {
		t.Fatalf("unexpected created site: %#v", created)
	}

	updated := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, map[string]any{
		"name": created.Name, "zone_id": created.ZoneID, "domains": created.Domains, "node_ids": created.Nodes,
		"primary_origin": created.PrimaryOrigin, "enabled": created.Enabled,
	})
	if !updated.Passthrough || updated.CacheGeneration != created.CacheGeneration {
		t.Fatalf("omitted passthrough did not preserve value: %#v", updated)
	}

	disabled := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, map[string]any{
		"name": updated.Name, "zone_id": updated.ZoneID, "domains": updated.Domains, "node_ids": updated.Nodes,
		"primary_origin": updated.PrimaryOrigin, "passthrough": false, "enabled": updated.Enabled,
	})
	if disabled.Passthrough || disabled.CacheGeneration != updated.CacheGeneration+1 {
		t.Fatalf("explicitly disabling passthrough did not update cache generation: %#v", disabled)
	}

	passthrough := requestSite(t, server, http.MethodPut, "/api/sites/"+created.ID, map[string]any{
		"name": disabled.Name, "zone_id": disabled.ZoneID, "domains": disabled.Domains, "node_ids": disabled.Nodes,
		"primary_origin": disabled.PrimaryOrigin, "passthrough": true, "enabled": disabled.Enabled,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/sites/"+passthrough.ID+"/invalidate-cache", nil)
	request.AddCookie(&http.Cookie{Name: "cdn_session", Value: "session-token"})
	request.Header.Set("X-CSRF-Token", "csrf-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("passthrough cache invalidation = %d %s", response.Code, response.Body.String())
	}
	afterInvalidation, _, err := database.GetSite(passthrough.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterInvalidation.CacheGeneration != passthrough.CacheGeneration {
		t.Fatalf("rejected cache invalidation changed generation: before=%d after=%d", passthrough.CacheGeneration, afterInvalidation.CacheGeneration)
	}
}

func requestSite(t *testing.T, server *Server, method, path string, input map[string]any) domain.Site {
	t.Helper()
	response := requestSiteResponse(t, server, method, path, input)
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, response.Code, response.Body.String())
	}
	var site domain.Site
	if err := json.NewDecoder(response.Body).Decode(&site); err != nil {
		t.Fatal(err)
	}
	return site
}

func requestSiteResponse(t *testing.T, server *Server, method, path string, input map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: "cdn_session", Value: "session-token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}
