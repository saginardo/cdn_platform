package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cdn-platform/internal/domain"
)

func TestNodeCacheStorageLifecycle(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	node, err := database.CreateNode("cache-storage", "203.0.113.90")
	if err != nil {
		t.Fatal(err)
	}
	collectedAt := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	initial := domain.CacheStorageUsage{UsedBytes: 1024, TotalBytes: 5 << 30, CollectedAt: collectedAt}
	if err := database.RecordNodeCacheStorage(node.ID, initial); err != nil {
		t.Fatal(err)
	}
	usage, err := database.GetNodeCacheStorage(node.ID)
	if err != nil || usage.UsedBytes != initial.UsedBytes || usage.TotalBytes != initial.TotalBytes || !usage.CollectedAt.Equal(collectedAt) {
		t.Fatalf("initial cache storage = %#v, err=%v", usage, err)
	}
	if err := database.RecordNodeCacheStorage(node.ID, domain.CacheStorageUsage{
		UsedBytes: 2048, TotalBytes: 5 << 30, CollectedAt: collectedAt.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if usage, err = database.GetNodeCacheStorage(node.ID); err != nil || usage.UsedBytes != initial.UsedBytes {
		t.Fatalf("older report replaced current storage: %#v, err=%v", usage, err)
	}
	if err := database.RecordNodeCacheStorage(node.ID, domain.CacheStorageUsage{
		UsedBytes: 3072, TotalBytes: 5 << 30, CollectedAt: collectedAt.Add(123 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordNodeCacheStorage(node.ID, domain.CacheStorageUsage{
		UsedBytes: 2048, TotalBytes: 5 << 30, CollectedAt: collectedAt.Add(12 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	if usage, err = database.GetNodeCacheStorage(node.ID); err != nil || usage.UsedBytes != 3072 {
		t.Fatalf("shorter fractional timestamp replaced newer storage: %#v, err=%v", usage, err)
	}
	if err := database.RecordNodeCacheStorage(node.ID, domain.CacheStorageUsage{
		UsedBytes: 4096, TotalBytes: 5 << 30, CollectedAt: collectedAt.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if usage, err = database.GetNodeCacheStorage(node.ID); err != nil || usage.UsedBytes != 4096 {
		t.Fatalf("newer cache storage = %#v, err=%v", usage, err)
	}
	if err := database.RecordNodeCacheStorage(node.ID, domain.CacheStorageUsage{UsedBytes: -1, TotalBytes: 5 << 30, CollectedAt: time.Now()}); err == nil {
		t.Fatal("negative cache storage usage was accepted")
	}
	if err := database.DeleteNode(node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetNodeCacheStorage(node.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cache storage survived node deletion: %v", err)
	}
}
