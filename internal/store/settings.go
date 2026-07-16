package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"cdn-platform/internal/domain"
)

const (
	SecretCloudflareAPIToken = "cloudflare_api_token"
	SecretSMTPPassword       = "smtp_password"
)

type SMTPSettings struct {
	Override    bool
	Enabled     bool
	Host        string
	Port        int
	Username    string
	FromAddress string
	Recipients  []string
	Security    string
}

type ControlSettings struct {
	DNSDefaultTTLSeconds int
	SMTP                 SMTPSettings
	UpdatedAt            *time.Time
}

func (s *Store) ControlSettings() (ControlSettings, error) {
	settings := ControlSettings{DNSDefaultTTLSeconds: domain.DefaultDNSTTLSeconds}
	var smtpOverride, smtpEnabled int
	var recipients, updatedAt string
	err := s.db.QueryRow(`SELECT dns_default_ttl_seconds, smtp_override, smtp_enabled, smtp_host, smtp_port, smtp_username, smtp_from_address, smtp_recipients_json, smtp_security, updated_at FROM control_settings WHERE id = 1`).
		Scan(&settings.DNSDefaultTTLSeconds, &smtpOverride, &smtpEnabled, &settings.SMTP.Host, &settings.SMTP.Port, &settings.SMTP.Username, &settings.SMTP.FromAddress, &recipients, &settings.SMTP.Security, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return ControlSettings{}, err
	}
	if err := domain.ValidateDNSTTLSeconds(settings.DNSDefaultTTLSeconds); err != nil {
		return ControlSettings{}, fmt.Errorf("stored DNS TTL: %w", err)
	}
	if err := json.Unmarshal([]byte(recipients), &settings.SMTP.Recipients); err != nil {
		return ControlSettings{}, fmt.Errorf("decode SMTP recipients: %w", err)
	}
	settings.SMTP.Override = smtpOverride != 0
	settings.SMTP.Enabled = smtpEnabled != 0
	parsed, err := parseTime(updatedAt)
	if err != nil {
		return ControlSettings{}, err
	}
	settings.UpdatedAt = &parsed
	return settings, nil
}

func (s *Store) SaveDNSDefaultTTL(seconds int) error {
	if err := domain.ValidateDNSTTLSeconds(seconds); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO control_settings(id, dns_default_ttl_seconds, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET dns_default_ttl_seconds=excluded.dns_default_ttl_seconds, updated_at=excluded.updated_at`, seconds, stamp(now()))
	return err
}

func (s *Store) SaveSMTPSettings(settings SMTPSettings, passwordCiphertext []byte, replacePassword bool) error {
	recipients, err := json.Marshal(settings.Recipients)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO control_settings(id, smtp_override, smtp_enabled, smtp_host, smtp_port, smtp_username, smtp_from_address, smtp_recipients_json, smtp_security, updated_at)
		VALUES (1, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET smtp_override=1, smtp_enabled=excluded.smtp_enabled, smtp_host=excluded.smtp_host, smtp_port=excluded.smtp_port, smtp_username=excluded.smtp_username, smtp_from_address=excluded.smtp_from_address, smtp_recipients_json=excluded.smtp_recipients_json, smtp_security=excluded.smtp_security, updated_at=excluded.updated_at`,
		boolInt(settings.Enabled), settings.Host, settings.Port, settings.Username, settings.FromAddress, string(recipients), settings.Security, stamp(now()))
	if err != nil {
		return err
	}
	if replacePassword {
		if len(passwordCiphertext) == 0 {
			if _, err := tx.Exec(`DELETE FROM secrets WHERE name = ?`, SecretSMTPPassword); err != nil {
				return err
			}
		} else if _, err := tx.Exec(`INSERT INTO secrets(name, ciphertext, updated_at) VALUES (?, ?, ?) ON CONFLICT(name) DO UPDATE SET ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`, SecretSMTPPassword, passwordCiphertext, stamp(now())); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ClearSMTPSettings() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE control_settings SET smtp_override=0, updated_at=? WHERE id=1`, stamp(now())); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM secrets WHERE name = ?`, SecretSMTPPassword); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteSecret(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("secret name is required")
	}
	_, err := s.db.Exec(`DELETE FROM secrets WHERE name = ?`, name)
	return err
}

func ReadSecret(path, name string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("secret name is required")
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='secrets'`).Scan(&tableCount); err != nil {
		return nil, err
	}
	if tableCount == 0 {
		return nil, ErrNotFound
	}
	var ciphertext []byte
	err = db.QueryRow(`SELECT ciphertext FROM secrets WHERE name = ?`, name).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ciphertext, err
}
