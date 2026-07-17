package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cdn-platform/internal/domain"
)

func (s *Store) RecordNodeCacheStorage(nodeID string, usage domain.CacheStorageUsage) error {
	if !domain.ValidCacheStorageUsage(usage) {
		return errors.New("invalid node cache storage report")
	}
	_, err := s.db.Exec(`INSERT INTO node_cache_storage(node_id, used_bytes, total_bytes, collected_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET used_bytes = excluded.used_bytes, total_bytes = excluded.total_bytes,
			collected_at = excluded.collected_at, updated_at = excluded.updated_at
		WHERE excluded.collected_at >= node_cache_storage.collected_at`,
		nodeID, usage.UsedBytes, usage.TotalBytes, cacheStorageStamp(usage.CollectedAt), stamp(now()))
	if err != nil {
		return fmt.Errorf("record node cache storage: %w", err)
	}
	return nil
}

func cacheStorageStamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func (s *Store) GetNodeCacheStorage(nodeID string) (domain.CacheStorageUsage, error) {
	var usage domain.CacheStorageUsage
	var collectedAt string
	err := s.db.QueryRow(`SELECT used_bytes, total_bytes, collected_at FROM node_cache_storage WHERE node_id = ?`, nodeID).
		Scan(&usage.UsedBytes, &usage.TotalBytes, &collectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CacheStorageUsage{}, ErrNotFound
	}
	if err != nil {
		return domain.CacheStorageUsage{}, err
	}
	usage.CollectedAt, err = parseTime(collectedAt)
	if err != nil {
		return domain.CacheStorageUsage{}, err
	}
	return usage, nil
}
