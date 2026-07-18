package store

import (
	"database/sql"
	"fmt"
	"sort"

	"cdn-platform/internal/domain"
)

type schemaMigration struct {
	Version int
	Name    string
	Apply   func(*sql.Tx) error
}

var schemaMigrations = []schemaMigration{
	{Version: 1, Name: "core-schema", Apply: migrateCoreSchema},
	{Version: 2, Name: "task-invariants", Apply: migrateTaskInvariants},
	{Version: 3, Name: "published-state-and-security-defaults", Apply: migratePublishedState},
	{Version: 4, Name: "nginx-config-fragments", Apply: migrateNginxFragments},
	{Version: 5, Name: "message-center", Apply: migrateMessageCenter},
	{Version: 6, Name: "message-dismissal", Apply: migrateMessageDismissal},
}

func LatestSchemaVersion() int {
	return schemaMigrations[len(schemaMigrations)-1].Version
}

func (s *Store) Migrate() error {
	if err := s.ensureMigrationTable(); err != nil {
		return err
	}
	applied, err := s.appliedMigrations()
	if err != nil {
		return err
	}
	for _, migration := range schemaMigrations {
		if name, ok := applied[migration.Version]; ok {
			if name != migration.Name {
				return fmt.Errorf("database migration %d is recorded as %q, expected %q", migration.Version, name, migration.Name)
			}
			continue
		}
		for version := 1; version < migration.Version; version++ {
			if _, ok := applied[version]; !ok {
				return fmt.Errorf("database migration history has a gap before version %d", migration.Version)
			}
		}
		if err := s.applyMigration(migration); err != nil {
			return err
		}
		applied[migration.Version] = migration.Name
	}
	latest := LatestSchemaVersion()
	for version := range applied {
		if version > latest {
			return fmt.Errorf("database schema version %d is newer than supported version %d", version, latest)
		}
	}
	// Certificate workers are process-scoped. This recovery is intentionally a
	// startup action rather than a one-time schema migration.
	if _, err := s.FailActiveCertificateTasks("certificate issuance interrupted by control-plane restart; retry Issue TLS"); err != nil {
		return fmt.Errorf("recover interrupted certificate tasks: %w", err)
	}
	return nil
}

func (s *Store) ensureMigrationTable() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration metadata transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration metadata: %w", err)
	}
	return nil
}

func (s *Store) appliedMigrations() (map[int]string, error) {
	rows, err := s.db.Query(`SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]string)
	versions := make([]int, 0)
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, fmt.Errorf("scan migration history: %w", err)
		}
		applied[version] = name
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	sort.Ints(versions)
	for index, version := range versions {
		if version != index+1 {
			return nil, fmt.Errorf("database migration history has a gap at version %d", index+1)
		}
	}
	return applied, nil
}

func (s *Store) applyMigration(migration schemaMigration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin database migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	defer tx.Rollback()
	if err := migration.Apply(tx); err != nil {
		return fmt.Errorf("apply database migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
		migration.Version, migration.Name, stamp(now())); err != nil {
		return fmt.Errorf("record database migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit database migration %d (%s): %w", migration.Version, migration.Name, err)
	}
	return nil
}

func migrateCoreSchema(tx *sql.Tx) error {
	if _, err := tx.Exec(initialSchema); err != nil {
		return fmt.Errorf("create core schema: %w", err)
	}
	columns := []struct {
		table      string
		name       string
		definition string
	}{
		{"sites", "published", "published INTEGER NOT NULL DEFAULT 0"},
		{"sites", "stream_paths_json", "stream_paths_json TEXT NOT NULL DEFAULT '[]'"},
		{"sites", "passthrough", "passthrough INTEGER NOT NULL DEFAULT 0"},
		{"sites", "client_max_body_size_mb", "client_max_body_size_mb INTEGER NOT NULL DEFAULT 128"},
		{"sites", "read_write_timeout_seconds", "read_write_timeout_seconds INTEGER NOT NULL DEFAULT 360"},
		{"sites", "dns_ttl_seconds", "dns_ttl_seconds INTEGER"},
		{"sites", "tcp_only", "tcp_only INTEGER NOT NULL DEFAULT 0"},
		{"sites", "tcp_forwards_json", "tcp_forwards_json TEXT NOT NULL DEFAULT '[]'"},
		{"sites", "deleting", "deleting INTEGER NOT NULL DEFAULT 0"},
		{"nodes", "applied_version", "applied_version INTEGER NOT NULL DEFAULT 0"},
		{"nodes", "capabilities_json", "capabilities_json TEXT NOT NULL DEFAULT '[]'"},
		{"nodes", "agent_sha256", "agent_sha256 TEXT NOT NULL DEFAULT ''"},
		{"nodes", "active_upgrade_task_id", "active_upgrade_task_id TEXT NOT NULL DEFAULT ''"},
		{"deployment_tasks", "deadline_at", "deadline_at TEXT"},
		{"control_settings", "backup_override", "backup_override INTEGER NOT NULL DEFAULT 0"},
		{"control_settings", "backup_repository", "backup_repository TEXT NOT NULL DEFAULT ''"},
		{"control_settings", "backup_access_key_id", "backup_access_key_id TEXT NOT NULL DEFAULT ''"},
		{"control_settings", "backup_region", "backup_region TEXT NOT NULL DEFAULT 'us-east-1'"},
		{"control_settings", "backup_time", "backup_time TEXT NOT NULL DEFAULT '03:25'"},
		{"control_settings", "backup_random_delay_seconds", "backup_random_delay_seconds INTEGER NOT NULL DEFAULT 1200"},
		// JSON null distinguishes a pre-capability state from an intentional empty listener set.
		{"node_states", "public_ports_json", "public_ports_json TEXT NOT NULL DEFAULT 'null'"},
		{"node_states", "nginx_stream_config", "nginx_stream_config TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := addColumnIfMissing(tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sites SET stream_paths_json = '[]' WHERE stream_paths_json <> '[]'`); err != nil {
		return fmt.Errorf("retire legacy stream paths: %w", err)
	}
	return nil
}

func migrateTaskInvariants(tx *sql.Tx) error {
	if _, err := tx.Exec(`UPDATE deployment_tasks
		SET status = ?, detail = ?, updated_at = ?
		WHERE kind = 'publish_site' AND status IN (?, ?, ?) AND deadline_at IS NULL`,
		domain.TaskFailed, "publish confirmation interrupted by control-plane upgrade; retry Publish", stamp(now()),
		domain.TaskQueued, domain.TaskDispatching, domain.TaskApplying); err != nil {
		return fmt.Errorf("migrate legacy publish tasks: %w", err)
	}
	indexes := []struct {
		name string
		sql  string
	}{
		{"active certificate task", `CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_active_certificate_site
			ON deployment_tasks(site_id)
			WHERE kind IN ('issue_certificate', 'renew_certificate') AND status IN ('queued', 'dispatching', 'applying')`},
		{"active publish task", `CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_active_publish_site
			ON deployment_tasks(site_id)
			WHERE kind = 'publish_site' AND status IN ('queued', 'dispatching', 'applying')`},
		{"active site deletion task", `CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_active_delete_site
			ON deployment_tasks(site_id)
			WHERE kind = 'delete_site' AND status IN ('queued', 'dispatching', 'applying')`},
		{"active node upgrade", `CREATE UNIQUE INDEX IF NOT EXISTS idx_node_upgrade_tasks_active
			ON node_upgrade_tasks(node_id)
			WHERE status IN ('queued', 'applying')`},
	}
	for _, index := range indexes {
		if _, err := tx.Exec(index.sql); err != nil {
			return fmt.Errorf("create %s index: %w", index.name, err)
		}
	}
	return nil
}

func migratePublishedState(tx *sql.Tx) error {
	if err := seedBuiltinSecurityPoliciesTx(tx); err != nil {
		return err
	}
	if err := backfillSitePublicationsTx(tx); err != nil {
		return err
	}
	return backfillSiteDomainsTx(tx)
}

func migrateNginxFragments(tx *sql.Tx) error {
	return addColumnIfMissing(tx, "node_states", "nginx_fragments_json", "nginx_fragments_json TEXT NOT NULL DEFAULT 'null'")
}

func migrateMessageCenter(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		severity TEXT NOT NULL,
		category TEXT NOT NULL,
		title TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '',
		source_type TEXT,
		source_id TEXT,
		source_status TEXT,
		resource_type TEXT NOT NULL DEFAULT '',
		resource_id TEXT NOT NULL DEFAULT '',
		read_at TEXT,
		created_at TEXT NOT NULL,
		UNIQUE(source_type, source_id, source_status)
	);
	CREATE INDEX IF NOT EXISTS idx_messages_created ON messages(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_messages_unread ON messages(read_at, created_at DESC);`)
	if err != nil {
		return fmt.Errorf("create message center schema: %w", err)
	}
	return nil
}

func migrateMessageDismissal(tx *sql.Tx) error {
	if err := addColumnIfMissing(tx, "messages", "dismissed_at", "dismissed_at TEXT"); err != nil {
		return err
	}
	_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_visible
		ON messages(dismissed_at, read_at, created_at DESC)`)
	if err != nil {
		return fmt.Errorf("create visible messages index: %w", err)
	}
	return nil
}

func addColumnIfMissing(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("inspect table %s: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect table %s: %w", table, err)
	}
	if found {
		return nil
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + definition); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}

func (s *Store) SchemaVersion() (int, error) {
	var version int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	return version, err
}
