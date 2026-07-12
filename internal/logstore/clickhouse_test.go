package logstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestRecentDecodesJSONEachRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Query().Get("query"), "FORMAT JSONEachRow") {
			t.Fatalf("unexpected query: %s", request.URL.Query().Get("query"))
		}
		_, _ = io.WriteString(response, "{\"timestamp\":\"2026-01-02T03:04:05Z\",\"node_id\":\"node\",\"site_id\":\"site\",\"client_ip\":\"203.0.113.5\",\"method\":\"GET\",\"path\":\"/a\",\"status\":200,\"bytes\":10,\"duration_ms\":2,\"upstream\":\"origin\",\"cache_status\":\"HIT\"}\n")
	}))
	defer server.Close()
	clickhouse := ClickHouse{Endpoint: server.URL}
	events, err := clickhouse.Recent(context.Background(), "site", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Path != "/a" || events[0].CacheStatus != "HIT" {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestMetricsDecodesJSONEachRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Query().Get("query"), "cdn_site_minute") {
			t.Fatalf("unexpected query: %s", request.URL.Query().Get("query"))
		}
		_, _ = io.WriteString(response, "{\"minute\":\"2026-01-02T03:04:00Z\",\"requests\":12,\"bytes\":1200,\"errors\":1,\"cache_hits\":9}\n")
	}))
	defer server.Close()
	metrics, err := (ClickHouse{Endpoint: server.URL}).Metrics(context.Background(), "site", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].Requests != 12 || metrics[0].CacheHits != 9 {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
}

func TestClickHouseTimeDecodesNativeDateTimeFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(response, "{\"timestamp\":\"2026-01-02 03:04:05.123\",\"node_id\":\"node\",\"site_id\":\"site\",\"client_ip\":\"203.0.113.5\",\"method\":\"GET\",\"path\":\"/a\",\"status\":200,\"bytes\":10,\"duration_ms\":2,\"upstream\":\"origin\",\"cache_status\":\"HIT\"}\n")
	}))
	defer server.Close()
	events, err := (ClickHouse{Endpoint: server.URL}).Recent(context.Background(), "site", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Timestamp.Location() != time.UTC || events[0].Timestamp.Nanosecond() != 123000000 {
		t.Fatalf("unexpected decoded event: %#v", events)
	}
}

func TestEnsureSchemaCreatesDatabaseOutsideTargetDatabase(t *testing.T) {
	var databases []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		databases = append(databases, request.URL.Query().Get("database"))
	}))
	defer server.Close()
	if err := (ClickHouse{Endpoint: server.URL, Database: "new_database"}).EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(databases) < 2 || databases[0] != "default" || databases[1] != "new_database" {
		t.Fatalf("unexpected schema databases: %#v", databases)
	}
}

func TestRequestOmitsBasicAuthWhenCredentialsAreUnset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if _, _, ok := request.BasicAuth(); ok {
			t.Fatal("unexpected basic authentication header")
		}
	}))
	defer server.Close()

	if err := (ClickHouse{Endpoint: server.URL}).EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAppendUsesClickHouseDateTimeFormat(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		contents, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = string(contents)
	}))
	defer server.Close()
	err := (ClickHouse{Endpoint: server.URL}).Append(context.Background(), []domain.AccessLogEvent{{Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC), SiteID: "site", NodeID: "node"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"timestamp":"2026-01-02 03:04:05.123"`) {
		t.Fatalf("unexpected insert body: %s", body)
	}
}
