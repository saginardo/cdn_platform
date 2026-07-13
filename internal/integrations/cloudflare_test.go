package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCloudflareDNSRejectsUnmanagedNameCollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected %s request", request.Method)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"success":     true,
			"result":      []DNSRecord{{ID: "manual", Name: "cdn.example.test", Content: "203.0.113.9", TTL: 60}},
			"result_info": map[string]any{"total_pages": 1},
		})
	}))
	defer server.Close()
	dns := CloudflareDNS{BaseURL: server.URL, Token: func() (string, error) { return "token", nil }}
	err := dns.Reconcile(context.Background(), "zone", "site=site-1", []DNSRecord{{Name: "cdn.example.test", Content: "203.0.113.10", Comment: "cdn-platform:site=site-1;node=node-1", TTL: 60}})
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected unmanaged collision error, got %v", err)
	}
}

func TestCloudflareDNSReconcilesOnlyCurrentOwnerAndPaginates(t *testing.T) {
	var created, deleted string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			page := request.URL.Query().Get("page")
			if page == "1" {
				_ = json.NewEncoder(response).Encode(map[string]any{
					"success":     true,
					"result":      []DNSRecord{{ID: "old", Name: "cdn.example.test", Content: "203.0.113.8", Comment: "cdn-platform:site=site-1;node=old", TTL: 60}},
					"result_info": map[string]any{"total_pages": 2},
				})
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"success":     true,
				"result":      []DNSRecord{{ID: "other", Name: "other.example.test", Content: "203.0.113.20", Comment: "cdn-platform:site=other;node=other", TTL: 60}},
				"result_info": map[string]any{"total_pages": 2},
			})
		case http.MethodPost:
			var record DNSRecord
			if err := json.NewDecoder(request.Body).Decode(&record); err != nil {
				t.Fatal(err)
			}
			created = record.Content
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "result": map[string]any{}})
		case http.MethodDelete:
			deleted = request.URL.Path
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "result": map[string]any{}})
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
	}))
	defer server.Close()
	dns := CloudflareDNS{BaseURL: server.URL, Token: func() (string, error) { return "token", nil }}
	err := dns.Reconcile(context.Background(), "zone", "site=site-1", []DNSRecord{{Name: "cdn.example.test", Content: "203.0.113.10", Comment: "cdn-platform:site=site-1;node=node-1", TTL: 60}})
	if err != nil {
		t.Fatal(err)
	}
	if created != "203.0.113.10" || !strings.HasSuffix(deleted, "/old") {
		t.Fatalf("unexpected reconciliation: created=%q deleted=%q", created, deleted)
	}
}

func TestCloudflareDNSRejectsCollisionEvenWhenCurrentRecordAlreadyMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected %s request", request.Method)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"success": true,
			"result": []DNSRecord{
				{ID: "managed", Name: "cdn.example.test", Content: "203.0.113.10", Comment: "cdn-platform:site=site-1;node=node-1", TTL: 60},
				{ID: "manual", Name: "cdn.example.test", Content: "203.0.113.20", TTL: 60},
			},
			"result_info": map[string]any{"total_pages": 1},
		})
	}))
	defer server.Close()
	dns := CloudflareDNS{BaseURL: server.URL, Token: func() (string, error) { return "token", nil }}
	err := dns.Reconcile(context.Background(), "zone", "site=site-1", []DNSRecord{{Name: "cdn.example.test", Content: "203.0.113.10", Comment: "cdn-platform:site=site-1;node=node-1", TTL: 60}})
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected conflict with manual record, got %v", err)
	}
}

func TestCloudflareDNSRejectsCNAMECollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("unexpected %s request", request.Method)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"success":     true,
			"result":      []DNSRecord{{ID: "alias", Type: "CNAME", Name: "cdn.example.test", Content: "elsewhere.example.test", TTL: 60}},
			"result_info": map[string]any{"total_pages": 1},
		})
	}))
	defer server.Close()
	dns := CloudflareDNS{BaseURL: server.URL, Token: func() (string, error) { return "token", nil }}
	err := dns.Reconcile(context.Background(), "zone", "site=site-1", []DNSRecord{{Name: "cdn.example.test", Content: "203.0.113.10", Comment: "cdn-platform:site=site-1;node=node-1", TTL: 60}})
	if err == nil || !strings.Contains(err.Error(), "CNAME") {
		t.Fatalf("expected CNAME collision error, got %v", err)
	}
}

func TestCloudflareDNSRemoveNodeDeletesOnlyExactManagedRecords(t *testing.T) {
	deleted := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			if got := request.URL.Query().Get("type"); got != "A" {
				t.Fatalf("record type filter = %q", got)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"success": true,
				"result": []DNSRecord{
					{ID: "target-1", Type: "A", Name: "a.example.test", Comment: "cdn-platform:site=one;node=node-1"},
					{ID: "target-2", Type: "A", Name: "b.example.test", Comment: "cdn-platform:node=node-1;site=two"},
					{ID: "similar", Type: "A", Name: "c.example.test", Comment: "cdn-platform:site=one;node=node-10"},
					{ID: "manual", Type: "A", Name: "d.example.test", Comment: "node=node-1"},
					{ID: "cname", Type: "CNAME", Name: "e.example.test", Comment: "cdn-platform:site=one;node=node-1"},
				},
				"result_info": map[string]any{"total_pages": 1},
			})
		case http.MethodDelete:
			deleted = append(deleted, request.URL.Path)
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "result": map[string]any{}})
		default:
			t.Fatalf("unexpected method %s", request.Method)
		}
	}))
	defer server.Close()

	dns := CloudflareDNS{BaseURL: server.URL, Token: func() (string, error) { return "token", nil }}
	if err := dns.RemoveNode(context.Background(), "zone", "node-1"); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 || !strings.HasSuffix(deleted[0], "/target-1") || !strings.HasSuffix(deleted[1], "/target-2") {
		t.Fatalf("deleted records = %#v", deleted)
	}
}
