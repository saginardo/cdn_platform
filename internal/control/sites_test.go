package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/store"
)

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
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, response.Code, response.Body.String())
	}
	var site domain.Site
	if err := json.NewDecoder(response.Body).Decode(&site); err != nil {
		t.Fatal(err)
	}
	return site
}
