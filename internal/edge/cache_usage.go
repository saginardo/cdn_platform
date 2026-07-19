package edge

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"cdn-platform/internal/domain"
)

const defaultCacheUsageInterval = 5 * time.Minute

type cacheUsageCollector struct {
	path       string
	totalBytes int64
	interval   time.Duration
	mu         sync.RWMutex
	usage      *domain.CacheStorageUsage
}

func newCacheUsageCollector(path string, totalBytes int64, interval time.Duration) *cacheUsageCollector {
	return &cacheUsageCollector{path: path, totalBytes: totalBytes, interval: interval}
}

func (c *cacheUsageCollector) Run(ctx context.Context) {
	_ = c.collect(ctx)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.collect(ctx)
		}
	}
}

func (c *cacheUsageCollector) collect(ctx context.Context) error {
	usedBytes, err := cacheDirectoryDiskBytes(ctx, c.path)
	if err != nil {
		return err
	}
	usage := &domain.CacheStorageUsage{
		UsedBytes: usedBytes, CollectedAt: time.Now().UTC(),
	}
	c.mu.Lock()
	usage.TotalBytes = c.totalBytes
	c.usage = usage
	c.mu.Unlock()
	return nil
}

func (c *cacheUsageCollector) SetTotalBytes(totalBytes int64) {
	if totalBytes <= 0 {
		return
	}
	c.mu.Lock()
	c.totalBytes = totalBytes
	if c.usage != nil {
		c.usage.TotalBytes = totalBytes
	}
	c.mu.Unlock()
}

func (c *cacheUsageCollector) Snapshot() *domain.CacheStorageUsage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.usage == nil {
		return nil
	}
	usage := *c.usage
	return &usage
}

func cacheDirectoryDiskBytes(ctx context.Context, root string) (int64, error) {
	var usedBytes int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		entryBytes := cacheEntryDiskBytes(info)
		if entryBytes > math.MaxInt64-usedBytes {
			usedBytes = math.MaxInt64
			return nil
		}
		usedBytes += entryBytes
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return usedBytes, err
}

func cacheEntryDiskBytes(info fs.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks > 0 {
		if stat.Blocks > math.MaxInt64/512 {
			return math.MaxInt64
		}
		return stat.Blocks * 512
	}
	if info.Size() < 0 {
		return 0
	}
	return info.Size()
}
