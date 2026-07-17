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
	"cdn-platform/internal/store"
)

func TestSecurityPoliciesRenderOnlyForCapableNodes(t *testing.T) {
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
	capable, err := database.CreateNode("security-capable", "203.0.113.81")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := database.CreateNode("security-legacy", "203.0.113.82")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeCapabilities(capable.ID, []string{domain.EdgeCapabilitySecurity}); err != nil {
		t.Fatal(err)
	}
	site, err := database.CreateSite(domain.Site{
		Name: "disabled-security-site", Domains: []string{"disabled.example.test"},
		Nodes: []string{capable.ID, legacy.ID}, PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true},
		Enabled: false,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	publisher := Publisher{Store: database, Cipher: cipher}
	if _, err := publisher.PublishSite(site.ID); err != nil {
		t.Fatal(err)
	}
	capableState, _, err := database.NodeState(capable.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacyState, _, err := database.NodeState(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capableState.NginxConfig, "cdn_security_policy_id") || !strings.Contains(capableState.NginxConfig, "return 444") {
		t.Fatalf("capable node lacks security configuration:\n%s", capableState.NginxConfig)
	}
	if strings.Contains(legacyState.NginxConfig, "cdn_security_policy_id") {
		t.Fatalf("legacy node received unsupported security configuration:\n%s", legacyState.NginxConfig)
	}
	overview, err := (&Server{Store: database}).securityOverview(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range overview.Nodes {
		if node.ID == capable.ID && !node.Configured {
			t.Fatalf("capable node was not marked configured: %#v", node)
		}
		if node.ID == legacy.ID && node.Configured {
			t.Fatalf("legacy node was marked configured: %#v", node)
		}
	}
}

func TestEdgeSecurityEventsReportsRejectedEventIndex(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("security-edge", "203.0.113.84")
	if err != nil {
		t.Fatal(err)
	}
	batch := domain.EdgeSecurityEventBatch{Events: []domain.SecurityEvent{{
		ID: "11111111-1111-4111-8111-111111111111", PolicyID: domain.DefaultSecurityPolicyID,
		ClientIP: "8.8.8.8", Path: "/not-sensitive", Action: domain.SecurityActionBan, ObservedAt: time.Now().UTC(),
	}}}
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/edge/v1/security-events", bytes.NewReader(payload))
	request = request.WithContext(context.WithValue(request.Context(), edgeContextKey{}, node.ID))
	response := httptest.NewRecorder()
	(&Server{Store: database}).edgeSecurityEvents(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var result struct {
		InvalidEventIndex *int `json:"invalid_event_index"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.InvalidEventIndex == nil || *result.InvalidEventIndex != 0 {
		t.Fatalf("rejection body = %s, err=%v", response.Body.String(), err)
	}
}

func TestSecurityOverviewReportsCoverage(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("security-edge", "203.0.113.83")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeCapabilities(node.ID, []string{domain.EdgeCapabilitySecurity}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	overview, err := server.securityOverview(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Policies) != 1 || len(overview.Nodes) != 1 || !overview.Nodes[0].Capable || overview.Nodes[0].Configured {
		t.Fatalf("security overview = %#v", overview)
	}
}
