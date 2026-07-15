package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/logstore"
)

type searchLogStore struct {
	logstore.Noop
	query logstore.LogQuery
	page  logstore.LogPage
	err   error
}

func (s *searchLogStore) Search(_ context.Context, query logstore.LogQuery) (logstore.LogPage, error) {
	s.query = query
	return s.page, s.err
}

func TestLogSearchRouteRequiresAdmin(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/logs", nil)
	(&Server{}).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected response status: %d", response.Code)
	}
}

func TestLogSearchParsesFiltersAndReturnsPage(t *testing.T) {
	from := time.Date(2026, 7, 15, 1, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	store := &searchLogStore{page: logstore.LogPage{
		Events:  []domain.AccessLogEvent{{Timestamp: to.Add(-time.Minute), SiteID: "site", Status: 404}},
		HasMore: true,
	}}
	values := url.Values{
		"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)},
		"site_id": {"site"}, "node_id": {"node"}, "method": {"get"}, "status": {"4xx"},
		"path": {"/API"}, "client_ip": {"2001:0db8::1"}, "cache_status": {"miss"}, "offset": {"100"},
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/logs?"+values.Encode(), nil)
	(&Server{Logs: store}).searchLogs(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected response status: %d, body: %s", response.Code, response.Body.String())
	}
	if store.query.From != from || store.query.To != to || store.query.SiteID != "site" || store.query.NodeID != "node" || store.query.Method != "GET" || store.query.StatusMin != 400 || store.query.StatusMax != 499 || store.query.Path != "/API" || store.query.ClientIP != "2001:db8::1" || store.query.CacheStatus != "MISS" || store.query.Offset != 100 || store.query.Limit != logSearchPageSize {
		t.Fatalf("unexpected query: %#v", store.query)
	}
	var payload logSearchResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Logs) != 1 || payload.Logs[0].Status != 404 || !payload.HasMore || payload.Offset != 100 || payload.PageSize != 100 || payload.From != from || payload.To != to {
		t.Fatalf("unexpected response: %#v", payload)
	}
}

func TestLogSearchDefaultsToPreviousHour(t *testing.T) {
	now := time.Date(2026, 7, 15, 8, 9, 10, 0, time.FixedZone("test", 8*60*60))
	query, err := parseLogSearchQuery(httptest.NewRequest(http.MethodGet, "/api/logs", nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if query.To != now.UTC() || query.From != now.UTC().Add(-time.Hour) || query.Offset != 0 || query.Limit != logSearchPageSize {
		t.Fatalf("unexpected defaults: %#v", query)
	}
}

func TestLogSearchRejectsInvalidFilters(t *testing.T) {
	tests := map[string]string{
		"invalid from":   "from=nope",
		"reversed time":  "from=2026-07-15T02%3A00%3A00Z&to=2026-07-15T01%3A00%3A00Z",
		"range too long": "from=2026-07-01T00%3A00%3A00Z&to=2026-07-15T00%3A00%3A00Z",
		"invalid status": "status=700",
		"invalid IP":     "client_ip=not-an-ip",
		"invalid offset": "offset=-1",
		"invalid method": "method=GET%20POST",
		"invalid cache":  "cache_status=UNKNOWN",
	}
	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/logs?"+query, nil)
			(&Server{}).searchLogs(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestLogSearchReturnsEmptyPageWithoutLogStore(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/logs?from=2026-07-15T00%3A00%3A00Z&to=2026-07-15T01%3A00%3A00Z", nil)
	(&Server{}).searchLogs(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"logs":[]`) {
		t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLogSearchMapsStoreFailureToBadGateway(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/logs?from=2026-07-15T00%3A00%3A00Z&to=2026-07-15T01%3A00%3A00Z", nil)
	(&Server{Logs: &searchLogStore{err: errors.New("clickhouse unavailable")}}).searchLogs(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("unexpected response: code=%d body=%s", response.Code, response.Body.String())
	}
}
