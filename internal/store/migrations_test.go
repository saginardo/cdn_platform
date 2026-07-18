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
