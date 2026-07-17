package control

import (
	"bytes"
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

func TestEdgeHeartbeatRecordsCacheStorageIndependentlyOfLogStats(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("cache-reporting-edge", "203.0.113.24")
	if err != nil {
		t.Fatal(err)
	}
	collectedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	payload, err := json.Marshal(heartbeatRequest{
		Capabilities: []string{domain.EdgeCapabilityCacheUsage},
		CacheStorage: &domain.CacheStorageUsage{UsedBytes: 3 << 30, TotalBytes: 5 << 30, CollectedAt: collectedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	heartbeat := httptest.NewRequest(http.MethodPost, "/api/edge/v1/heartbeat", bytes.NewReader(payload))
	heartbeat = heartbeat.WithContext(context.WithValue(heartbeat.Context(), edgeContextKey{}, node.ID))
	heartbeatResponse := httptest.NewRecorder()
	server.heartbeat(heartbeatResponse, heartbeat)
	if heartbeatResponse.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body=%s", heartbeatResponse.Code, heartbeatResponse.Body.String())
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
	if cache.Available || !cache.Storage.Available || cache.Storage.UsedBytes != 3<<30 || cache.Storage.TotalBytes != 5<<30 || !cache.Storage.Stale {
		t.Fatalf("cache response = %#v", cache)
	}
	futurePayload, err := json.Marshal(heartbeatRequest{
		CacheStorage: &domain.CacheStorageUsage{UsedBytes: 4 << 30, TotalBytes: 5 << 30, CollectedAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	future := httptest.NewRequest(http.MethodPost, "/api/edge/v1/heartbeat", bytes.NewReader(futurePayload))
	future = future.WithContext(context.WithValue(future.Context(), edgeContextKey{}, node.ID))
	futureResponse := httptest.NewRecorder()
	server.heartbeat(futureResponse, future)
	if futureResponse.Code != http.StatusOK {
		t.Fatalf("future heartbeat status = %d, body=%s", futureResponse.Code, futureResponse.Body.String())
	}
	if usage, err := database.GetNodeCacheStorage(node.ID); err != nil || usage.UsedBytes != 3<<30 {
		t.Fatalf("future report replaced cache storage: %#v, err=%v", usage, err)
	}

	invalidPayload, err := json.Marshal(heartbeatRequest{
		CacheStorage: &domain.CacheStorageUsage{UsedBytes: -1, TotalBytes: 5 << 30, CollectedAt: time.Now()},
	})
	if err != nil {
		t.Fatal(err)
	}
	invalid := httptest.NewRequest(http.MethodPost, "/api/edge/v1/heartbeat", bytes.NewReader(invalidPayload))
	invalid = invalid.WithContext(context.WithValue(invalid.Context(), edgeContextKey{}, node.ID))
	invalidResponse := httptest.NewRecorder()
	server.heartbeat(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid heartbeat status = %d, body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestEdgeHeartbeatRecordsMachineStatusForNodeDetail(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("machine-reporting-edge", "203.0.113.81")
	if err != nil {
		t.Fatal(err)
	}
	report := controlTestMachineStatus(time.Now().UTC().Add(-time.Minute).Truncate(time.Second))
	payload, err := json.Marshal(heartbeatRequest{
		Capabilities: []string{domain.EdgeCapabilityMachineStatus}, MachineStatus: &report,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	heartbeat := httptest.NewRequest(http.MethodPost, "/api/edge/v1/heartbeat", bytes.NewReader(payload))
	heartbeat = heartbeat.WithContext(context.WithValue(heartbeat.Context(), edgeContextKey{}, node.ID))
	heartbeatResponse := httptest.NewRecorder()
	server.heartbeat(heartbeatResponse, heartbeat)
	if heartbeatResponse.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body=%s", heartbeatResponse.Code, heartbeatResponse.Body.String())
	}

	detailRequest := httptest.NewRequest(http.MethodGet, "/api/nodes/"+node.ID, nil)
	detailRequest.SetPathValue("id", node.ID)
	detailResponse := httptest.NewRecorder()
	server.nodeDetail(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail nodeDetailResponse
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.Machine.Available || detail.Machine.Stale || detail.Machine.Report == nil || detail.Machine.Report.Version != "13.5" || detail.Machine.Report.NetworkRXBytesPerSec != 2000 {
		t.Fatalf("machine detail = %#v", detail.Machine)
	}

	futureReport := controlTestMachineStatus(time.Now().UTC().Add(time.Hour))
	futureReport.Version = "future"
	futurePayload, err := json.Marshal(heartbeatRequest{MachineStatus: &futureReport})
	if err != nil {
		t.Fatal(err)
	}
	future := httptest.NewRequest(http.MethodPost, "/api/edge/v1/heartbeat", bytes.NewReader(futurePayload))
	future = future.WithContext(context.WithValue(future.Context(), edgeContextKey{}, node.ID))
	futureResponse := httptest.NewRecorder()
	server.heartbeat(futureResponse, future)
	if futureResponse.Code != http.StatusOK {
		t.Fatalf("future heartbeat status = %d, body=%s", futureResponse.Code, futureResponse.Body.String())
	}
	stored, err := database.GetNodeMachineStatus(node.ID)
	if err != nil || stored.Version != "13.5" {
		t.Fatalf("future report replaced machine status: %#v, err=%v", stored, err)
	}

	invalidReport := controlTestMachineStatus(time.Now().UTC())
	invalidReport.DiskUsedBytes = invalidReport.DiskTotalBytes + 1
	invalidPayload, err := json.Marshal(heartbeatRequest{MachineStatus: &invalidReport})
	if err != nil {
		t.Fatal(err)
	}
	invalid := httptest.NewRequest(http.MethodPost, "/api/edge/v1/heartbeat", bytes.NewReader(invalidPayload))
	invalid = invalid.WithContext(context.WithValue(invalid.Context(), edgeContextKey{}, node.ID))
	invalidResponse := httptest.NewRecorder()
	server.heartbeat(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid heartbeat status = %d, body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestNodeDetailExplainsUnavailableMachineStatus(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	legacy, err := database.CreateNode("legacy-machine-edge", "203.0.113.82")
	if err != nil {
		t.Fatal(err)
	}
	capable, err := database.CreateNode("capable-machine-edge", "203.0.113.83")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetNodeCapabilities(capable.ID, []string{domain.EdgeCapabilityMachineStatus}); err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: database}
	legacyStatus := server.nodeMachineStatus(legacy, time.Now().UTC())
	if legacyStatus.Available || legacyStatus.UnavailableReason != "升级边缘代理后可查看机器状态" {
		t.Fatalf("legacy machine status = %#v", legacyStatus)
	}
	capable, err = database.GetNode(capable.ID)
	if err != nil {
		t.Fatal(err)
	}
	capableStatus := server.nodeMachineStatus(capable, time.Now().UTC())
	if capableStatus.Available || capableStatus.UnavailableReason != "等待边缘节点首次上报机器状态" {
		t.Fatalf("capable machine status = %#v", capableStatus)
	}
}

func controlTestMachineStatus(collectedAt time.Time) domain.MachineStatus {
	return domain.MachineStatus{
		Distribution: "Debian GNU/Linux", Version: "13.5", UptimeSeconds: 86400,
		Load1: 0.1, Load5: 0.2, Load15: 0.3, CPUUsagePercent: 25, CPULogicalCores: 4,
		MemoryUsedBytes: 2 << 30, MemoryTotalBytes: 4 << 30,
		DiskUsedBytes: 20 << 30, DiskTotalBytes: 100 << 30,
		NetworkInterface: "eth0", NetworkRXBytesPerSec: 2000, NetworkTXBytesPerSec: 1000,
		SampleSeconds: 30, CollectedAt: collectedAt,
	}
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
	if err := database.SetNodeCapabilities(node.ID, []string{domain.EdgeCapabilityOnlineUpgrade, domain.EdgeCapabilityCacheUsage}); err != nil {
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
	storageCollectedAt := time.Now().UTC().Add(-time.Minute)
	if err := database.RecordNodeCacheStorage(node.ID, domain.CacheStorageUsage{
		UsedBytes: 2 << 30, TotalBytes: 5 << 30, CollectedAt: storageCollectedAt,
	}); err != nil {
		t.Fatal(err)
	}
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
	if !cache.Storage.Available || cache.Storage.UsedBytes != 2<<30 || cache.Storage.TotalBytes != 5<<30 || cache.Storage.CollectedAt == nil || cache.Storage.Stale {
		t.Fatalf("unexpected cache storage: %#v", cache.Storage)
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
	if cache.Available || cache.UnavailableReason != "缓存统计暂不可用" || cache.Statuses == nil || cache.Storage.Available || cache.Storage.UnavailableReason != "升级边缘代理后可查看缓存空间" {
		t.Fatalf("unexpected unavailable cache response: %#v", cache)
	}
}
