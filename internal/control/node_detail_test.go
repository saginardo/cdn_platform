package control

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/logstore"
	"cdn-platform/internal/store"
)

type nodeCacheLogStore struct {
	logstore.Noop
	buckets []logstore.NodeCacheBucket
	err     error
}

func TestNodeDetailRouteRequiresAdmin(t *testing.T) {
	for _, path := range []string{"/api/nodes/node-1", "/api/nodes/node-1/cache-status"} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		(&Server{}).Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s: unexpected response status: %d", path, response.Code)
		}
	}
}

func (s nodeCacheLogStore) NodeCache(context.Context, string, time.Time, time.Time) ([]logstore.NodeCacheBucket, error) {
	return s.buckets, s.err
}

func TestNodeDetailReturnsManagementContextAndIndependentCacheStatus(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("detail-edge", "203.0.113.21")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeCapabilities(node.ID, []string{domain.EdgeCapabilityOnlineUpgrade}); err != nil {
		t.Fatal(err)
	}
	agentDigest := strings.Repeat("a", 64)
	if err := database.HeartbeatWithAgent(node.ID, 7, "", nil, agentDigest, ""); err != nil {
		t.Fatal(err)
	}
	assigned, err := database.CreateSite(domain.Site{
		Name: "Assigned Site", Domains: []string{"assigned.example.test"}, Nodes: []string{node.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone")
	if err != nil {
		t.Fatal(err)
	}
	otherNode, err := database.CreateNode("other-edge", "203.0.113.23")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSite(domain.Site{
		Name: "Other Site", Domains: []string{"other.example.test"}, Nodes: []string{otherNode.ID},
		PrimaryOrigin: domain.Origin{URL: "https://origin.example.test", Enabled: true}, Enabled: true,
	}, "zone"); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 7, 17, 2, 0, 0, 0, time.UTC)
	logs := nodeCacheLogStore{buckets: []logstore.NodeCacheBucket{
		{Status: "hit", Requests: 60, Bytes: 6000, LastSeenAt: seen},
		{Status: "STALE", Requests: 5, Bytes: 500, LastSeenAt: seen.Add(time.Minute)},
		{Status: "MISS", Requests: 20, Bytes: 2000, LastSeenAt: seen},
		{Status: "EXPIRED", Requests: 5, Bytes: 500, LastSeenAt: seen},
		{Status: "BYPASS", Requests: 8, Bytes: 800, LastSeenAt: seen},
		{Status: "", Requests: 2, Bytes: 200, LastSeenAt: seen},
	}}
	server := &Server{Store: database, Logs: logs, EdgeBinarySHA256: agentDigest}
	request := httptest.NewRequest(http.MethodGet, "/api/nodes/"+node.ID, nil)
	request.SetPathValue("id", node.ID)
	response := httptest.NewRecorder()
	server.nodeDetail(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", response.Code, response.Body.String())
	}
	var detail nodeDetailResponse
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Node.ID != node.ID || detail.Node.AppliedVersion != 7 || len(detail.Sites) != 1 || detail.Sites[0].ID != assigned.ID || !detail.Sites[0].CacheEnabled {
		t.Fatalf("unexpected node context: %#v", detail)
	}
	cacheRequest := httptest.NewRequest(http.MethodGet, "/api/nodes/"+node.ID+"/cache-status", nil)
	cacheRequest.SetPathValue("id", node.ID)
	cacheResponse := httptest.NewRecorder()
	server.nodeCacheStatus(cacheResponse, cacheRequest)
	if cacheResponse.Code != http.StatusOK {
		t.Fatalf("cache status = %d, body=%s", cacheResponse.Code, cacheResponse.Body.String())
	}
	var cache nodeCacheStatusResponse
	if err := json.Unmarshal(cacheResponse.Body.Bytes(), &cache); err != nil {
		t.Fatal(err)
	}
	if !cache.Available || cache.Requests != 100 || cache.Bytes != 10000 || cache.CacheHits != 65 || cache.CacheMisses != 25 || cache.CacheLookups != 90 || cache.Bypasses != 8 || cache.Uncached != 2 {
		t.Fatalf("unexpected cache summary: %#v", cache)
	}
	if math.Abs(cache.HitRate-(65.0/90.0)) > 0.000001 || cache.LastSeenAt == nil || !cache.LastSeenAt.Equal(seen.Add(time.Minute)) {
		t.Fatalf("unexpected cache rate or timestamp: %#v", cache)
	}
	if len(cache.Statuses) != 6 || cache.Statuses[0].Status != "HIT" || cache.Statuses[1].Status != "MISS" || cache.Statuses[2].Status != "BYPASS" || cache.Statuses[5].Status != "UNCACHED" {
		t.Fatalf("unexpected cache status order: %#v", cache.Statuses)
	}
}

func TestNodeCacheStatusDegradesWithoutBlockingNodeDetail(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("cache-unavailable", "203.0.113.22")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database, Logs: nodeCacheLogStore{err: errors.New("clickhouse offline")}}
	request := httptest.NewRequest(http.MethodGet, "/api/nodes/"+node.ID, nil)
	request.SetPathValue("id", node.ID)
	response := httptest.NewRecorder()
	server.nodeDetail(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", response.Code, response.Body.String())
	}
	var detail nodeDetailResponse
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Node.ID != node.ID {
		t.Fatalf("unexpected node detail: %#v", detail)
	}
	cacheRequest := httptest.NewRequest(http.MethodGet, "/api/nodes/"+node.ID+"/cache-status", nil)
	cacheRequest.SetPathValue("id", node.ID)
	cacheResponse := httptest.NewRecorder()
	server.nodeCacheStatus(cacheResponse, cacheRequest)
	if cacheResponse.Code != http.StatusOK {
		t.Fatalf("cache status = %d, body=%s", cacheResponse.Code, cacheResponse.Body.String())
	}
	var cache nodeCacheStatusResponse
	if err := json.Unmarshal(cacheResponse.Body.Bytes(), &cache); err != nil {
		t.Fatal(err)
	}
	if cache.Available || cache.UnavailableReason != "缓存统计暂不可用" || cache.Statuses == nil {
		t.Fatalf("unexpected unavailable cache response: %#v", cache)
	}
}
