package control

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"cdn-platform/internal/domain"
	"cdn-platform/internal/logstore"
	"cdn-platform/internal/store"
)

const (
	nodeCacheWindow           = 24 * time.Hour
	nodeCacheStorageFreshness = 15 * time.Minute
)

type nodeCacheStatusBucket struct {
	Status   string `json:"status"`
	Requests uint64 `json:"requests"`
	Bytes    int64  `json:"bytes"`
}

type nodeCacheStorageStatus struct {
	Available         bool       `json:"available"`
	UnavailableReason string     `json:"unavailable_reason,omitempty"`
	UsedBytes         int64      `json:"used_bytes"`
	TotalBytes        int64      `json:"total_bytes"`
	CollectedAt       *time.Time `json:"collected_at,omitempty"`
	Stale             bool       `json:"stale"`
}

type nodeCacheStatusResponse struct {
	Available         bool                    `json:"available"`
	UnavailableReason string                  `json:"unavailable_reason,omitempty"`
	From              time.Time               `json:"from"`
	To                time.Time               `json:"to"`
	LastSeenAt        *time.Time              `json:"last_seen_at,omitempty"`
	Requests          uint64                  `json:"requests"`
	Bytes             int64                   `json:"bytes"`
	CacheLookups      uint64                  `json:"cache_lookups"`
	CacheHits         uint64                  `json:"cache_hits"`
	CacheMisses       uint64                  `json:"cache_misses"`
	Bypasses          uint64                  `json:"bypasses"`
	Uncached          uint64                  `json:"uncached"`
	HitRate           float64                 `json:"hit_rate"`
	Statuses          []nodeCacheStatusBucket `json:"statuses"`
	Storage           nodeCacheStorageStatus  `json:"storage"`
}

type nodeSiteSummary struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Domains      []string `json:"domains"`
	Enabled      bool     `json:"enabled"`
	Published    bool     `json:"published"`
	CacheEnabled bool     `json:"cache_enabled"`
}

type nodeDetailResponse struct {
	Node  nodeUpgradeStatusResponse `json:"node"`
	Sites []nodeSiteSummary         `json:"sites"`
}

func (s *Server) nodeDetail(response http.ResponseWriter, request *http.Request) {
	if err := s.Store.ReconcileNodeUpgrades(); err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	node, err := s.Store.GetNode(request.PathValue("id"))
	if err != nil {
		writeStoreError(response, err)
		return
	}
	status, err := s.buildNodeUpgradeStatus(node)
	if err != nil {
		writeError(response, http.StatusInternalServerError, err)
		return
	}
	configuredSites, err := s.Store.ListSites()
	if err != nil {
		writeStoreError(response, err)
		return
	}
	sites := make([]nodeSiteSummary, 0)
	for _, site := range configuredSites {
		if !containsNode(site.Nodes, node.ID) {
			continue
		}
		sites = append(sites, nodeSiteSummary{
			ID: site.ID, Name: site.Name, Domains: append([]string{}, site.Domains...),
			Enabled: site.Enabled, Published: site.Published, CacheEnabled: siteCacheEnabled(site),
		})
	}

	writeJSON(response, http.StatusOK, nodeDetailResponse{Node: status, Sites: sites})
}

func (s *Server) nodeCacheStatus(response http.ResponseWriter, request *http.Request) {
	nodeID := request.PathValue("id")
	node, err := s.Store.GetNode(nodeID)
	if err != nil {
		writeStoreError(response, err)
		return
	}
	to := time.Now().UTC().Truncate(time.Second)
	from := to.Add(-nodeCacheWindow)
	cache := buildNodeCacheStatus(from, to, nil, false, "访问日志存储未启用")
	if s.Logs != nil {
		buckets, cacheErr := s.Logs.NodeCache(request.Context(), nodeID, from, to)
		switch {
		case cacheErr == nil:
			cache = buildNodeCacheStatus(from, to, buckets, true, "")
		case errors.Is(cacheErr, logstore.ErrUnavailable):
			cache = buildNodeCacheStatus(from, to, nil, false, "访问日志存储未启用")
		default:
			cache = buildNodeCacheStatus(from, to, nil, false, "缓存统计暂不可用")
			if s.Logger != nil {
				s.Logger.Warn("node cache status unavailable", "node_id", nodeID, "error", cacheErr)
			}
		}
	}
	cache.Storage = s.nodeCacheStorageStatus(node, to)
	writeJSON(response, http.StatusOK, cache)
}

func (s *Server) nodeCacheStorageStatus(node domain.Node, at time.Time) nodeCacheStorageStatus {
	usage, err := s.Store.GetNodeCacheStorage(node.ID)
	if err == nil {
		collectedAt := usage.CollectedAt.UTC()
		return nodeCacheStorageStatus{
			Available: true, UsedBytes: usage.UsedBytes, TotalBytes: usage.TotalBytes,
			CollectedAt: &collectedAt, Stale: collectedAt.Before(at.Add(-nodeCacheStorageFreshness)),
		}
	}
	if !errors.Is(err, store.ErrNotFound) {
		if s.Logger != nil {
			s.Logger.Warn("node cache storage unavailable", "node_id", node.ID, "error", err)
		}
		return nodeCacheStorageStatus{UnavailableReason: "缓存空间上报暂不可用"}
	}
	for _, capability := range node.Capabilities {
		if capability == domain.EdgeCapabilityCacheUsage {
			return nodeCacheStorageStatus{UnavailableReason: "等待边缘节点首次采集缓存空间"}
		}
	}
	return nodeCacheStorageStatus{UnavailableReason: "升级边缘代理后可查看缓存空间"}
}

func buildNodeCacheStatus(from, to time.Time, buckets []logstore.NodeCacheBucket, available bool, unavailableReason string) nodeCacheStatusResponse {
	type aggregate struct {
		requests uint64
		bytes    int64
	}
	aggregates := make(map[string]aggregate)
	var lastSeenAt *time.Time
	for _, bucket := range buckets {
		status := strings.ToUpper(strings.TrimSpace(bucket.Status))
		if status == "" {
			status = "UNCACHED"
		}
		current := aggregates[status]
		current.requests += bucket.Requests
		current.bytes += bucket.Bytes
		aggregates[status] = current
		if lastSeenAt == nil || bucket.LastSeenAt.After(*lastSeenAt) {
			value := bucket.LastSeenAt.UTC()
			lastSeenAt = &value
		}
	}

	result := nodeCacheStatusResponse{
		Available: available, UnavailableReason: unavailableReason, From: from, To: to,
		LastSeenAt: lastSeenAt, Statuses: make([]nodeCacheStatusBucket, 0, len(aggregates)),
	}
	for status, values := range aggregates {
		result.Requests += values.requests
		result.Bytes += values.bytes
		switch status {
		case "HIT", "STALE", "UPDATING", "REVALIDATED":
			result.CacheHits += values.requests
		case "MISS", "EXPIRED":
			result.CacheMisses += values.requests
		case "BYPASS":
			result.Bypasses += values.requests
		case "UNCACHED":
			result.Uncached += values.requests
		}
		result.Statuses = append(result.Statuses, nodeCacheStatusBucket{Status: status, Requests: values.requests, Bytes: values.bytes})
	}
	result.CacheLookups = result.CacheHits + result.CacheMisses
	if result.CacheLookups > 0 {
		result.HitRate = float64(result.CacheHits) / float64(result.CacheLookups)
	}
	order := map[string]int{"HIT": 0, "MISS": 1, "BYPASS": 2, "EXPIRED": 3, "STALE": 4, "UPDATING": 5, "REVALIDATED": 6, "UNCACHED": 7}
	sort.Slice(result.Statuses, func(i, j int) bool {
		left, leftKnown := order[result.Statuses[i].Status]
		right, rightKnown := order[result.Statuses[j].Status]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown && left != right {
			return left < right
		}
		return result.Statuses[i].Status < result.Statuses[j].Status
	})
	return result
}

func containsNode(nodeIDs []string, nodeID string) bool {
	for _, candidate := range nodeIDs {
		if candidate == nodeID {
			return true
		}
	}
	return false
}

func siteCacheEnabled(site domain.Site) bool {
	if site.Passthrough || site.TCPOnly {
		return false
	}
	parsed, err := url.Parse(site.PrimaryOrigin.URL)
	if err != nil {
		return false
	}
	scheme := domain.ProxyScheme(strings.ToLower(parsed.Scheme))
	return scheme == "http" || scheme == "https"
}
