package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
