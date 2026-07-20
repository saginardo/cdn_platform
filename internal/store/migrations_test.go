package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrationsRecordCurrentVersionAndRemainIdempotent(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	version, err := database.SchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if want := schemaMigrations[len(schemaMigrations)-1].Version; version != want {
		t.Fatalf("schema version = %d, want %d", version, want)
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(schemaMigrations) {
		t.Fatalf("migration history count = %d, want %d", count, len(schemaMigrations))
	}
}

func TestBrandingLogoMigrationAddsLegacyColumn(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.db.Exec(`ALTER TABLE control_settings DROP COLUMN brand_logo_data_url`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`DELETE FROM schema_migrations WHERE version = 11`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	found, err := columnExists(database.db, "control_settings", "brand_logo_data_url")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("branding logo migration did not add control_settings.brand_logo_data_url")
	}
}

func TestFailedMigrationRollsBackSchemaAndVersionRecord(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	wanted := errors.New("stop migration")
	migration := schemaMigration{
		Version: 99,
		Name:    "transaction-rollback-test",
		Apply: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`CREATE TABLE migration_rollback_probe (id INTEGER PRIMARY KEY)`); err != nil {
				return err
			}
			return wanted
		},
	}
	if err := database.applyMigration(migration); !errors.Is(err, wanted) {
		t.Fatalf("failed migration error = %v", err)
	}
	var tableCount int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'migration_rollback_probe'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 0 {
		t.Fatal("failed migration left its table behind")
	}
	var versionCount int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 99`).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 0 {
		t.Fatal("failed migration recorded a version")
	}
}

func TestMigrationHistoryRejectsGaps(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.db.Exec(`DELETE FROM schema_migrations WHERE version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(); err == nil {
		t.Fatal("migration history gap was accepted")
	}
}

func TestLatestMigrationDropsMachineStatusAndAddsCacheDefaults(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.db.Exec(`CREATE TABLE node_machine_status (node_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`DELETE FROM schema_migrations WHERE version >= 8`); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	var machineTableCount int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'node_machine_status'`).Scan(&machineTableCount); err != nil {
		t.Fatal(err)
	}
	if machineTableCount != 0 {
		t.Fatal("machine status table remains after migration")
	}
	settings, err := database.ControlSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.CacheDefaultSizeGB != 1 {
		t.Fatalf("cache default = %d, want 1", settings.CacheDefaultSizeGB)
	}
	for table, column := range map[string]string{
		"sites": "cache_max_size_gb", "nodes": "cache_max_size_gb", "node_states": "cache_max_bytes",
	} {
		found, err := columnExists(database.db, table, column)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("missing %s.%s after migration", table, column)
		}
	}
}

func TestRateLimitBanEscalationMigrationAddsLegacyColumns(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`PRAGMA foreign_keys = ON;
		CREATE TABLE rate_limit_policies (id TEXT PRIMARY KEY);
		CREATE TABLE security_bans (ip TEXT PRIMARY KEY);
		CREATE TABLE security_events (id TEXT PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateRateLimitBanEscalation(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for table, columns := range map[string][]string{
		"rate_limit_policies": {"ban_enabled", "ban_after_consecutive_429", "ban_duration_seconds"},
		"security_bans":       {"rate_limit_policy_id"},
		"security_events":     {"rate_limit_policy_id"},
	} {
		for _, column := range columns {
			found, err := columnExists(database, table, column)
			if err != nil {
				t.Fatal(err)
			}
			if !found {
				t.Fatalf("missing migrated column %s.%s", table, column)
			}
		}
	}
	if _, err := database.Exec(`INSERT INTO rate_limit_policies(id) VALUES ('policy')`); err != nil {
		t.Fatal(err)
	}
	var enabled, after, duration int
	if err := database.QueryRow(`SELECT ban_enabled, ban_after_consecutive_429, ban_duration_seconds
		FROM rate_limit_policies WHERE id = 'policy'`).Scan(&enabled, &after, &duration); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || after != 3 || duration != 3600 {
		t.Fatalf("migrated defaults = enabled:%d after:%d duration:%d", enabled, after, duration)
	}
}

func TestNodeCacheLimitMigrationAddsNodeOverrideAndClearsSiteOverrides(t *testing.T) {
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE nodes (id TEXT PRIMARY KEY);
		CREATE TABLE sites (id TEXT PRIMARY KEY, cache_max_size_gb INTEGER);
		INSERT INTO sites(id, cache_max_size_gb) VALUES ('site-1', 7);`); err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateNodeCacheLimits(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	found, err := columnExists(database, "nodes", "cache_max_size_gb")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("node cache override column was not added")
	}
	var legacyOverride sql.NullInt64
	if err := database.QueryRow(`SELECT cache_max_size_gb FROM sites WHERE id = 'site-1'`).Scan(&legacyOverride); err != nil {
		t.Fatal(err)
	}
	if legacyOverride.Valid {
		t.Fatalf("legacy site cache override remains set to %d", legacyOverride.Int64)
	}
}

func columnExists(database *sql.DB, table, column string) (bool, error) {
	rows, err := database.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
