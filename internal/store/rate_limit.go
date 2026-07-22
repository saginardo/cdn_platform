package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"simple_cdn/internal/domain"
)

const (
	rateLimitPolicyColumns = `id, name, enabled, requests_per_second,
		response_condition_enabled, response_status_classes_json, ban_enabled,
		ban_after_consecutive_429, ban_duration_seconds, created_at, updated_at`
	maxRateLimitPolicies = 50
)

func (s *Store) ListRateLimitPolicies() ([]domain.RateLimitPolicy, error) {
	rows, err := s.db.Query(`SELECT ` + rateLimitPolicyColumns + ` FROM rate_limit_policies ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	policies := make([]domain.RateLimitPolicy, 0)
	for rows.Next() {
		policy, err := scanRateLimitPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (s *Store) RateLimitPolicy(id string) (domain.RateLimitPolicy, error) {
	return scanRateLimitPolicy(s.db.QueryRow(`SELECT `+rateLimitPolicyColumns+` FROM rate_limit_policies WHERE id = ?`, id))
}

func (s *Store) CreateRateLimitPolicy(policy domain.RateLimitPolicy) (domain.RateLimitPolicy, error) {
	var err error
	policy, err = domain.NormalizeRateLimitPolicy(policy)
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	policy.ID = uuid.NewString()
	policy.CreatedAt = now()
	policy.UpdatedAt = policy.CreatedAt
	classes, err := json.Marshal(policy.ResponseStatusClasses)
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM rate_limit_policies`).Scan(&count); err != nil {
		return domain.RateLimitPolicy{}, err
	}
	if count >= maxRateLimitPolicies {
		return domain.RateLimitPolicy{}, fmt.Errorf("rate limit policy limit of %d reached", maxRateLimitPolicies)
	}
	_, err = tx.Exec(`INSERT INTO rate_limit_policies(id, name, enabled, requests_per_second,
		response_condition_enabled, response_status_classes_json, ban_enabled,
		ban_after_consecutive_429, ban_duration_seconds, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, policy.ID, policy.Name, boolInt(policy.Enabled),
		policy.RequestsPerSecond, boolInt(policy.ResponseConditionEnabled), string(classes),
		boolInt(policy.BanEnabled), policy.BanAfterConsecutive429, policy.BanDurationSeconds,
		stamp(policy.CreatedAt), stamp(policy.UpdatedAt))
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.RateLimitPolicy{}, err
	}
	return policy, nil
}

func (s *Store) UpdateRateLimitPolicy(id string, policy domain.RateLimitPolicy) (domain.RateLimitPolicy, error) {
	var err error
	policy, err = domain.NormalizeRateLimitPolicy(policy)
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	classes, err := json.Marshal(policy.ResponseStatusClasses)
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	updatedAt := now()
	result, err := s.db.Exec(`UPDATE rate_limit_policies SET name = ?, enabled = ?, requests_per_second = ?,
		response_condition_enabled = ?, response_status_classes_json = ?, ban_enabled = ?,
		ban_after_consecutive_429 = ?, ban_duration_seconds = ?, updated_at = ? WHERE id = ?`,
		policy.Name, boolInt(policy.Enabled), policy.RequestsPerSecond,
		boolInt(policy.ResponseConditionEnabled), string(classes), boolInt(policy.BanEnabled),
		policy.BanAfterConsecutive429, policy.BanDurationSeconds, stamp(updatedAt), id)
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	if changed != 1 {
		return domain.RateLimitPolicy{}, ErrNotFound
	}
	return s.RateLimitPolicy(id)
}

func (s *Store) DeleteRateLimitPolicy(id string) error {
	result, err := s.db.Exec(`DELETE FROM rate_limit_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func scanRateLimitPolicy(row scanner) (domain.RateLimitPolicy, error) {
	var policy domain.RateLimitPolicy
	var enabled, responseConditionEnabled, banEnabled int
	var classesJSON, createdAt, updatedAt string
	err := row.Scan(&policy.ID, &policy.Name, &enabled, &policy.RequestsPerSecond,
		&responseConditionEnabled, &classesJSON, &banEnabled, &policy.BanAfterConsecutive429,
		&policy.BanDurationSeconds, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RateLimitPolicy{}, ErrNotFound
	}
	if err != nil {
		return domain.RateLimitPolicy{}, err
	}
	policy.Enabled = enabled != 0
	policy.ResponseConditionEnabled = responseConditionEnabled != 0
	policy.BanEnabled = banEnabled != 0
	policy.Key = domain.RateLimitKeyClientIP
	if err := json.Unmarshal([]byte(classesJSON), &policy.ResponseStatusClasses); err != nil {
		return domain.RateLimitPolicy{}, fmt.Errorf("decode rate limit response classes: %w", err)
	}
	policy, err = domain.NormalizeRateLimitPolicy(policy)
	if err != nil {
		return domain.RateLimitPolicy{}, fmt.Errorf("decode rate limit policy: %w", err)
	}
	if policy.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.RateLimitPolicy{}, err
	}
	if policy.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.RateLimitPolicy{}, err
	}
	return policy, nil
}
