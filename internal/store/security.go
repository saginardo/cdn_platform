package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"cdn-platform/internal/domain"
	"github.com/google/uuid"
)

const securityPolicyColumns = `id, name, enabled, pattern, action, ban_duration_seconds,
	priority, created_at, updated_at`

const (
	maxSecurityPolicies = 100
	maxSecurityEvents   = 100000
	maxSecurityBans     = 50000
)

type SecurityEventInputError struct {
	Index int
	Err   error
}

type securityEventPolicy struct {
	ID                 string
	Name               string
	Pattern            string
	Action             domain.SecurityPolicyAction
	BanDurationSeconds int
	RateLimit          bool
}

type securityEventPolicyValidationError struct {
	Err error
}

func (e *securityEventPolicyValidationError) Error() string { return e.Err.Error() }
func (e *securityEventPolicyValidationError) Unwrap() error { return e.Err }

func (e *SecurityEventInputError) Error() string { return e.Err.Error() }
func (e *SecurityEventInputError) Unwrap() error { return e.Err }

func (s *Store) ListSecurityPolicies() ([]domain.SecurityPolicy, error) {
	rows, err := s.db.Query(`SELECT ` + securityPolicyColumns + ` FROM security_policies ORDER BY priority, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	policies := make([]domain.SecurityPolicy, 0)
	for rows.Next() {
		policy, err := scanSecurityPolicy(rows)
		if err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

func (s *Store) SecurityPolicy(id string) (domain.SecurityPolicy, error) {
	return scanSecurityPolicy(s.db.QueryRow(`SELECT `+securityPolicyColumns+` FROM security_policies WHERE id = ?`, id))
}

func (s *Store) CreateSecurityPolicy(policy domain.SecurityPolicy) (domain.SecurityPolicy, error) {
	var err error
	policy, err = domain.NormalizeSecurityPolicy(policy)
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	policy.ID = uuid.NewString()
	policy.CreatedAt = now()
	policy.UpdatedAt = policy.CreatedAt
	tx, err := s.db.Begin()
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM security_policies`).Scan(&count); err != nil {
		return domain.SecurityPolicy{}, err
	}
	if count >= maxSecurityPolicies {
		return domain.SecurityPolicy{}, fmt.Errorf("security policy limit of %d reached", maxSecurityPolicies)
	}
	_, err = tx.Exec(`INSERT INTO security_policies(id, name, enabled, pattern, action,
		ban_duration_seconds, priority, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		policy.ID, policy.Name, boolInt(policy.Enabled), policy.Pattern, policy.Action,
		policy.BanDurationSeconds, policy.Priority, stamp(policy.CreatedAt), stamp(policy.UpdatedAt))
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.SecurityPolicy{}, err
	}
	return policy, nil
}

func (s *Store) UpdateSecurityPolicy(id string, policy domain.SecurityPolicy) (domain.SecurityPolicy, error) {
	var err error
	policy, err = domain.NormalizeSecurityPolicy(policy)
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	updatedAt := now()
	result, err := s.db.Exec(`UPDATE security_policies SET name = ?, enabled = ?, pattern = ?, action = ?,
		ban_duration_seconds = ?, priority = ?, updated_at = ? WHERE id = ?`, policy.Name,
		boolInt(policy.Enabled), policy.Pattern, policy.Action, policy.BanDurationSeconds,
		policy.Priority, stamp(updatedAt), id)
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	if changed != 1 {
		return domain.SecurityPolicy{}, ErrNotFound
	}
	return s.SecurityPolicy(id)
}

func (s *Store) DeleteSecurityPolicy(id string) error {
	if domain.IsBuiltinSecurityPolicyID(id) {
		return errors.New("the built-in security policy cannot be deleted; disable it instead")
	}
	result, err := s.db.Exec(`DELETE FROM security_policies WHERE id = ?`, id)
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

func (s *Store) RecordSecurityEvents(nodeID string, events []domain.SecurityEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	if len(events) > 200 {
		return 0, errors.New("security event batch exceeds 200 events")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	createdAt := now()
	accepted := 0
	invalid := func(index int, err error) (int, error) {
		return accepted, &SecurityEventInputError{Index: index, Err: err}
	}
	for index, input := range events {
		eventID := strings.TrimSpace(input.ID)
		if _, err := uuid.Parse(eventID); err != nil {
			return invalid(index, errors.New("security event ID is invalid"))
		}
		var existingNodeID sql.NullString
		err := tx.QueryRow(`SELECT node_id FROM security_events WHERE id = ?`, eventID).Scan(&existingNodeID)
		if err == nil {
			if !existingNodeID.Valid || existingNodeID.String != nodeID {
				return invalid(index, errors.New("security event ID belongs to another node"))
			}
			accepted++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return accepted, err
		}
		policy, err := securityEventPolicyForInput(tx, input)
		if err != nil {
			var validationError *securityEventPolicyValidationError
			if errors.As(err, &validationError) {
				return invalid(index, validationError.Err)
			}
			return accepted, err
		}
		address, err := netip.ParseAddr(strings.TrimSpace(input.ClientIP))
		if err != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() {
			return invalid(index, errors.New("security event client IP is not a public IPv4 address"))
		}
		path := strings.TrimSpace(input.Path)
		if path == "" || len(path) > 2048 || len(input.Host) > 255 || len(input.Method) > 16 {
			return invalid(index, errors.New("security event fields are invalid"))
		}
		if !policy.RateLimit {
			matcher, err := domain.CompileSecurityPattern(policy.Pattern)
			if err != nil || !matcher.MatchString(path) {
				return invalid(index, errors.New("security event path does not match its policy"))
			}
		}
		observedAt := input.ObservedAt.UTC()
		if observedAt.IsZero() || observedAt.After(createdAt.Add(2*time.Minute)) {
			observedAt = createdAt
		} else if observedAt.Before(createdAt.Add(-7 * 24 * time.Hour)) {
			return invalid(index, errors.New("security event is older than the retention window"))
		} else if !observedAt.Before(createdAt.Add(-10 * time.Minute)) {
			observedAt = createdAt
		}
		var banExpiresAt *time.Time
		if policy.Action == domain.SecurityActionBan {
			expiresAt := observedAt.Add(time.Duration(policy.BanDurationSeconds) * time.Second)
			banExpiresAt = &expiresAt
			if expiresAt.After(createdAt) {
				securityPolicyID, rateLimitPolicyID := securityEventPolicyReferences(policy)
				_, err = tx.Exec(`INSERT INTO security_bans(ip, policy_id, rate_limit_policy_id, policy_name, trigger_node_id,
					host, path, method, expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT(ip) DO UPDATE SET policy_id=excluded.policy_id,
					rate_limit_policy_id=excluded.rate_limit_policy_id, policy_name=excluded.policy_name,
					trigger_node_id=excluded.trigger_node_id, host=excluded.host, path=excluded.path,
					method=excluded.method, expires_at=CASE WHEN excluded.expires_at > security_bans.expires_at
					THEN excluded.expires_at ELSE security_bans.expires_at END, updated_at=excluded.updated_at`,
					address.String(), securityPolicyID, rateLimitPolicyID, policy.Name, nodeID, strings.TrimSpace(input.Host), path,
					strings.ToUpper(strings.TrimSpace(input.Method)), stamp(expiresAt), stamp(createdAt), stamp(createdAt))
				if err != nil {
					return accepted, err
				}
			}
		}
		var encodedExpiry any
		if banExpiresAt != nil {
			encodedExpiry = stamp(*banExpiresAt)
		}
		securityPolicyID, rateLimitPolicyID := securityEventPolicyReferences(policy)
		_, err = tx.Exec(`INSERT INTO security_events(id, node_id, policy_id, rate_limit_policy_id, policy_name, client_ip,
			host, path, method, action, observed_at, ban_expires_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, eventID, nodeID, securityPolicyID,
			rateLimitPolicyID, policy.Name, address.String(), strings.TrimSpace(input.Host), path,
			strings.ToUpper(strings.TrimSpace(input.Method)), policy.Action, stamp(observedAt), encodedExpiry, stamp(createdAt))
		if err != nil {
			return accepted, err
		}
		accepted++
	}
	if _, err := tx.Exec(`DELETE FROM security_events WHERE created_at < ?`, stamp(createdAt.Add(-7*24*time.Hour))); err != nil {
		return accepted, err
	}
	if _, err := tx.Exec(`DELETE FROM security_events WHERE id IN (
		SELECT id FROM security_events ORDER BY created_at DESC, id DESC LIMIT -1 OFFSET ?)`, maxSecurityEvents); err != nil {
		return accepted, err
	}
	if _, err := tx.Exec(`DELETE FROM security_bans WHERE ip IN (
		SELECT ip FROM security_bans ORDER BY updated_at DESC, ip LIMIT -1 OFFSET ?)`, maxSecurityBans); err != nil {
		return accepted, err
	}
	if err := tx.Commit(); err != nil {
		return accepted, err
	}
	return accepted, nil
}

func securityEventPolicyForInput(tx *sql.Tx, input domain.SecurityEvent) (securityEventPolicy, error) {
	policy, err := scanSecurityPolicy(tx.QueryRow(`SELECT `+securityPolicyColumns+` FROM security_policies WHERE id = ?`, input.PolicyID))
	if err == nil {
		if !policy.Enabled || input.Action != policy.Action {
			return securityEventPolicy{}, &securityEventPolicyValidationError{
				Err: errors.New("security event does not match an enabled policy"),
			}
		}
		return securityEventPolicy{
			ID: policy.ID, Name: policy.Name, Pattern: policy.Pattern, Action: policy.Action,
			BanDurationSeconds: policy.BanDurationSeconds,
		}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return securityEventPolicy{}, err
	}
	ratePolicy, err := scanRateLimitPolicy(tx.QueryRow(`SELECT `+rateLimitPolicyColumns+` FROM rate_limit_policies WHERE id = ?`, input.PolicyID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return securityEventPolicy{}, &securityEventPolicyValidationError{
				Err: errors.New("security event policy no longer exists"),
			}
		}
		return securityEventPolicy{}, err
	}
	if !ratePolicy.Enabled || !ratePolicy.BanEnabled || input.Action != domain.SecurityActionBan {
		return securityEventPolicy{}, &securityEventPolicyValidationError{
			Err: errors.New("security event does not match an enabled policy"),
		}
	}
	return securityEventPolicy{
		ID: ratePolicy.ID, Name: ratePolicy.Name, Action: domain.SecurityActionBan,
		BanDurationSeconds: ratePolicy.BanDurationSeconds, RateLimit: true,
	}, nil
}

func securityEventPolicyReferences(policy securityEventPolicy) (any, any) {
	if policy.RateLimit {
		return nil, policy.ID
	}
	return policy.ID, nil
}

func (s *Store) ListActiveSecurityBans() ([]domain.SecurityBan, error) {
	return s.listActiveSecurityBans(-1)
}

func (s *Store) ListActiveSecurityBansLimit(limit int) ([]domain.SecurityBan, error) {
	if limit < 1 || limit > 5000 {
		limit = 500
	}
	return s.listActiveSecurityBans(limit)
}

func (s *Store) listActiveSecurityBans(limit int) ([]domain.SecurityBan, error) {
	if err := s.ReconcileSecurity(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT ip, COALESCE(policy_id, rate_limit_policy_id, ''), policy_name,
		COALESCE(trigger_node_id, ''), host, path, method, expires_at, created_at, updated_at
		FROM security_bans ORDER BY expires_at DESC, ip LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bans := make([]domain.SecurityBan, 0)
	for rows.Next() {
		ban, err := scanSecurityBan(rows)
		if err != nil {
			return nil, err
		}
		bans = append(bans, ban)
	}
	return bans, rows.Err()
}

func (s *Store) CountActiveSecurityBans() (int, error) {
	if err := s.ReconcileSecurity(); err != nil {
		return 0, err
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM security_bans`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) ListRecentSecurityEvents(limit int) ([]domain.SecurityEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, COALESCE(node_id, ''), COALESCE(policy_id, rate_limit_policy_id, ''), policy_name,
		client_ip, host, path, method, action, observed_at, ban_expires_at, created_at
		FROM security_events ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.SecurityEvent, 0, limit)
	for rows.Next() {
		event, err := scanSecurityEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) DeleteSecurityBan(ip string) error {
	address, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || !address.Is4() {
		return errors.New("invalid security ban IP")
	}
	result, err := s.db.Exec(`DELETE FROM security_bans WHERE ip = ?`, address.String())
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

func (s *Store) ReconcileSecurity() error {
	cutoff := now()
	_, err := s.db.Exec(`DELETE FROM security_bans WHERE expires_at <= ?`, stamp(cutoff))
	return err
}

func scanSecurityPolicy(row scanner) (domain.SecurityPolicy, error) {
	var policy domain.SecurityPolicy
	var enabled int
	var createdAt, updatedAt string
	err := row.Scan(&policy.ID, &policy.Name, &enabled, &policy.Pattern, &policy.Action,
		&policy.BanDurationSeconds, &policy.Priority, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SecurityPolicy{}, ErrNotFound
	}
	if err != nil {
		return domain.SecurityPolicy{}, err
	}
	policy.Enabled = enabled != 0
	policy.Builtin = domain.IsBuiltinSecurityPolicyID(policy.ID)
	if policy.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.SecurityPolicy{}, err
	}
	if policy.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.SecurityPolicy{}, err
	}
	return policy, nil
}

func scanSecurityBan(row scanner) (domain.SecurityBan, error) {
	var ban domain.SecurityBan
	var expiresAt, createdAt, updatedAt string
	if err := row.Scan(&ban.IP, &ban.PolicyID, &ban.PolicyName, &ban.TriggerNodeID,
		&ban.Host, &ban.Path, &ban.Method, &expiresAt, &createdAt, &updatedAt); err != nil {
		return domain.SecurityBan{}, err
	}
	var err error
	if ban.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return domain.SecurityBan{}, err
	}
	if ban.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.SecurityBan{}, err
	}
	if ban.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.SecurityBan{}, err
	}
	return ban, nil
}

func scanSecurityEvent(row scanner) (domain.SecurityEvent, error) {
	var event domain.SecurityEvent
	var observedAt, createdAt string
	var banExpiresAt sql.NullString
	if err := row.Scan(&event.ID, &event.NodeID, &event.PolicyID, &event.PolicyName, &event.ClientIP,
		&event.Host, &event.Path, &event.Method, &event.Action, &observedAt, &banExpiresAt, &createdAt); err != nil {
		return domain.SecurityEvent{}, err
	}
	var err error
	if event.ObservedAt, err = parseTime(observedAt); err != nil {
		return domain.SecurityEvent{}, err
	}
	if event.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.SecurityEvent{}, err
	}
	if banExpiresAt.Valid {
		expiresAt, err := parseTime(banExpiresAt.String)
		if err != nil {
			return domain.SecurityEvent{}, err
		}
		event.BanExpiresAt = &expiresAt
	}
	return event, nil
}
