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
	if _, err := database.db.Exec(`DELETE FROM schema_migrations WHERE version = 8`); err != nil {
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
		"sites": "cache_max_size_gb", "node_states": "cache_max_bytes",
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
