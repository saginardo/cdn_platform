package logstore

import (
	"context"
	"fmt"
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

func TestSearchAppliesFiltersAndReportsMoreRows(t *testing.T) {
	from := time.Date(2026, 7, 15, 1, 2, 3, 4000000, time.UTC)
	to := from.Add(time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		for _, expected := range []string{
			"PREWHERE timestamp >= {from:DateTime64(3)} AND timestamp < {to:DateTime64(3)}",
			"site_id = {site_id:String}", "node_id = {node_id:String}", "method = {method:String}",
			"status >= {status_min:UInt16}", "status <= {status_max:UInt16}",
			"positionCaseInsensitive(path, {path:String}) > 0", "client_ip = {client_ip:String}",
			"cache_status = {cache_status:String}", "LIMIT 3 OFFSET 100",
		} {
			if !strings.Contains(query, expected) {
				t.Fatalf("query does not contain %q: %s", expected, query)
			}
		}
		parameters := request.URL.Query()
		expectedParameters := map[string]string{
			"param_from": "2026-07-15 01:02:03.004", "param_to": "2026-07-15 02:02:03.004",
			"param_site_id": "site", "param_node_id": "node", "param_method": "GET",
			"param_status_min": "400", "param_status_max": "499", "param_path": "/api",
			"param_client_ip": "203.0.113.5", "param_cache_status": "MISS",
		}
		for key, expected := range expectedParameters {
			if got := parameters.Get(key); got != expected {
				t.Fatalf("unexpected %s: got %q, want %q", key, got, expected)
			}
		}
		for index := 0; index < 3; index++ {
			_, _ = io.WriteString(response, fmt.Sprintf("{\"timestamp\":\"2026-07-15T01:02:0%dZ\",\"node_id\":\"node\",\"site_id\":\"site\",\"client_ip\":\"203.0.113.5\",\"method\":\"GET\",\"path\":\"/api\",\"status\":404,\"bytes\":10,\"duration_ms\":2,\"upstream\":\"origin\",\"cache_status\":\"MISS\"}\n", index))
		}
	}))
	defer server.Close()

	page, err := (ClickHouse{Endpoint: server.URL}).Search(context.Background(), LogQuery{
		From: from, To: to, SiteID: "site", NodeID: "node", Method: "GET",
		StatusMin: 400, StatusMax: 499, Path: "/api", ClientIP: "203.0.113.5",
		CacheStatus: "MISS", Offset: 100, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || !page.HasMore {
		t.Fatalf("unexpected page: %#v", page)
	}
}

func TestSearchUsesDefaultsAndNeverEmitsNegativeOffset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		if !strings.Contains(query, "LIMIT 101 OFFSET 0") {
			t.Fatalf("unexpected query: %s", query)
		}
	}))
	defer server.Close()
	page, err := (ClickHouse{Endpoint: server.URL}).Search(context.Background(), LogQuery{Offset: -1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Events == nil || len(page.Events) != 0 || page.HasMore {
		t.Fatalf("unexpected empty page: %#v", page)
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

func TestOverviewDecodesHourlyStatusRows(t *testing.T) {
	from := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query().Get("query")
		if !strings.Contains(query, "toStartOfHour(timestamp)") || !strings.Contains(query, "GROUP BY hour, site_id, status") {
			t.Fatalf("unexpected query: %s", query)
		}
		if request.URL.Query().Get("param_from") != "2026-01-02 03:04:05" || request.URL.Query().Get("param_to") != "2026-01-03 03:04:05" {
			t.Fatalf("unexpected time parameters: %s", request.URL.RawQuery)
		}
		_, _ = io.WriteString(response, "{\"hour\":\"2026-01-02T04:00:00Z\",\"site_id\":\"site\",\"status\":404,\"requests\":\"7\",\"bytes\":\"700\"}\n")
	}))
	defer server.Close()
	buckets, err := (ClickHouse{Endpoint: server.URL}).Overview(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].SiteID != "site" || buckets[0].Status != 404 || buckets[0].Requests != 7 || buckets[0].Bytes != 700 {
		t.Fatalf("unexpected overview buckets: %#v", buckets)
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
