package edge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"simple_cdn/internal/domain"
)

func TestCacheUsageCollectorReportsDiskUsage(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, "a", "b"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a", "first.cache"), []byte(strings.Repeat("a", 7000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "a", "b", "second.cache"), []byte(strings.Repeat("b", 3000)), 0o600); err != nil {
		t.Fatal(err)
	}

	collector := newCacheUsageCollector(directory, 5<<30, time.Minute)
	if collector.Snapshot() != nil {
		t.Fatal("collector exposed a snapshot before its first scan")
	}
	before := time.Now().UTC()
	if err := collector.collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	usage := collector.Snapshot()
	if usage == nil || usage.UsedBytes < 10000 || usage.TotalBytes != 5<<30 || usage.CollectedAt.Before(before) {
		t.Fatalf("cache usage = %#v", usage)
	}
	usage.UsedBytes = 1
	if collector.Snapshot().UsedBytes == 1 {
		t.Fatal("snapshot mutation changed the collector state")
	}
	collector.SetTotalBytes(7 << 30)
	if updated := collector.Snapshot(); updated == nil || updated.TotalBytes != 7<<30 {
		t.Fatalf("updated cache capacity = %#v", updated)
	}
}

func TestCacheUsageCollectorTreatsMissingCacheAsEmpty(t *testing.T) {
	collector := newCacheUsageCollector(filepath.Join(t.TempDir(), "not-created"), 5<<30, time.Minute)
	if err := collector.collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	usage := collector.Snapshot()
	if usage == nil || usage.UsedBytes != 0 || usage.TotalBytes != 5<<30 {
		t.Fatalf("missing cache usage = %#v", usage)
	}
}

func TestHeartbeatIncludesCacheStorageUsageAndCapability(t *testing.T) {
	cacheDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(cacheDirectory, "object.cache"), []byte(strings.Repeat("x", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}
	var heartbeat struct {
		Capabilities []string                  `json:"capabilities"`
		AgentVersion string                    `json:"agent_version"`
		Storage      *domain.CacheStorageUsage `json:"cache_storage"`
	}
	client := &http.Client{Transport: upgradeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	})}
	agent, err := New(Config{
		ControlURL: "https://control.example.test", StateDir: t.TempDir(),
		CertificateDir: t.TempDir(),
		AgentVersion:   "9.8.7", AgentSHA256: strings.Repeat("a", 64), HTTPClient: client, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.cacheUsage = newCacheUsageCollector(cacheDirectory, 5<<30, time.Minute)
	if err := agent.cacheUsage.collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := agent.Heartbeat(context.Background(), 1, "", nil); err != nil {
		t.Fatal(err)
	}
	if heartbeat.Storage == nil || heartbeat.Storage.UsedBytes == 0 || heartbeat.Storage.TotalBytes != 5<<30 {
		t.Fatalf("heartbeat storage = %#v", heartbeat.Storage)
	}
	if heartbeat.AgentVersion != "9.8.7" {
		t.Fatalf("heartbeat agent version = %q", heartbeat.AgentVersion)
	}
	found := false
	for _, capability := range heartbeat.Capabilities {
		found = found || capability == domain.EdgeCapabilityCacheUsage
	}
	if !found {
		t.Fatalf("heartbeat capabilities = %#v", heartbeat.Capabilities)
	}
}
