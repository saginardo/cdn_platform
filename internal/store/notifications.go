package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type NotificationDeliveryState struct {
	Key        string
	Active     bool
	LastSentAt time.Time
}

func (s *Store) NotificationDeliveryState(key string) (NotificationDeliveryState, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return NotificationDeliveryState{}, errors.New("notification delivery key is required")
	}
	var state NotificationDeliveryState
	var active int
	var lastSentAt string
	err := s.db.QueryRow(`SELECT key, active, last_sent_at FROM notification_delivery_state WHERE key = ?`, key).
		Scan(&state.Key, &active, &lastSentAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationDeliveryState{}, ErrNotFound
	}
	if err != nil {
		return NotificationDeliveryState{}, err
	}
	state.Active = active != 0
	state.LastSentAt, err = parseTime(lastSentAt)
	return state, err
}

func (s *Store) MarkNotificationDelivered(key string, active bool, sentAt time.Time) error {
	key = strings.TrimSpace(key)
	if key == "" || sentAt.IsZero() {
		return errors.New("notification delivery key and timestamp are required")
	}
	_, err := s.db.Exec(`INSERT INTO notification_delivery_state(key, active, last_sent_at, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(key) DO UPDATE SET active=excluded.active,
		last_sent_at=excluded.last_sent_at, updated_at=excluded.updated_at`,
		key, boolInt(active), stamp(sentAt.UTC()), stamp(now()))
	return err
}

func (s *Store) ResolveNotificationDelivery(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("notification delivery key is required")
	}
	_, err := s.db.Exec(`UPDATE notification_delivery_state SET active = 0, updated_at = ? WHERE key = ?`, stamp(now()), key)
	return err
}
