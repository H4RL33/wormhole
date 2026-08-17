package localstore

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const GatewaySchemaVersion = 5

const gatewayMigrationLedgerDDL = `CREATE TABLE gateway_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

const ensureGatewayMigrationLedgerDDL = `CREATE TABLE IF NOT EXISTS gateway_schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

var migrationNamePattern = regexp.MustCompile(`^(\d{6})_[a-z0-9_]+\.sql$`)

//go:embed migrations/*.sql
var gatewayMigrationFiles embed.FS

type gatewayMigration struct {
	version int
	name    string
	sql     string
}

// applyGatewayMigrations applies the embedded Gateway schema migrations.
func applyGatewayMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadGatewayMigrations()
	if err != nil {
		return err
	}
	return applyGatewayMigrationSet(ctx, db, migrations)
}

// applyGatewayMigrationSet applies each missing version in a separate immediate
// transaction so a later migration cannot roll back an already durable version.
func applyGatewayMigrationSet(ctx context.Context, db *sql.DB, migrations []gatewayMigration) error {
	if err := validateGatewayMigrationSet(migrations); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("localstore: acquire migration connection: %w", err)
	}
	defer conn.Close()
	if err := withGatewayMigrationTransaction(ctx, conn, func() error {
		return prepareGatewayMigrationLedger(ctx, conn, len(migrations))
	}); err != nil {
		return err
	}
	for _, migration := range migrations {
		if err := withGatewayMigrationTransaction(ctx, conn, func() error {
			if err := prepareGatewayMigrationLedger(ctx, conn, len(migrations)); err != nil {
				return err
			}
			applied, err := gatewayAppliedVersions(ctx, conn)
			if err != nil {
				return err
			}
			if applied[migration.version] {
				return nil
			}
			for version := 1; version < migration.version; version++ {
				if !applied[version] {
					return fmt.Errorf("localstore: gateway migration %s requires missing version %d", migration.name, version)
				}
			}
			migrationSQL := migration.sql
			if migration.version == 1 {
				var found bool
				migrationSQL, found = strings.CutPrefix(migrationSQL, gatewayMigrationLedgerDDL)
				if !found {
					return fmt.Errorf("localstore: migration %s does not declare the canonical ledger", migration.name)
				}
			}
			if _, err := conn.ExecContext(ctx, migrationSQL); err != nil {
				return fmt.Errorf("localstore: apply gateway migration %s: %w", migration.name, err)
			}
			if err := validateGatewayForeignKeys(ctx, conn); err != nil {
				return fmt.Errorf("localstore: validate gateway migration %s foreign keys: %w", migration.name, err)
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO gateway_schema_migrations(version) VALUES (?)`, migration.version); err != nil {
				return fmt.Errorf("localstore: record gateway migration %s: %w", migration.name, err)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateGatewayForeignKeys(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("inspect foreign keys: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	var table, parent string
	var rowID any
	var foreignKeyID int
	if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
		return fmt.Errorf("scan foreign key violation: %w", err)
	}
	return fmt.Errorf("foreign key violation table=%s rowid=%v parent=%s foreign_key=%d", table, rowID, parent, foreignKeyID)
}

func withGatewayMigrationTransaction(ctx context.Context, conn *sql.Conn, operation func() error) (err error) {
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("localstore: begin gateway migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if err := operation(); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("localstore: commit gateway migration: %w", err)
	}
	committed = true
	return nil
}

func prepareGatewayMigrationLedger(ctx context.Context, conn *sql.Conn, supportedVersion int) error {
	if _, err := conn.ExecContext(ctx, ensureGatewayMigrationLedgerDDL); err != nil {
		return fmt.Errorf("localstore: create gateway migration ledger: %w", err)
	}
	if err := validateGatewayMigrationLedger(ctx, conn); err != nil {
		return err
	}
	applied, err := gatewayAppliedVersions(ctx, conn)
	if err != nil {
		return err
	}
	maxApplied := 0
	for version := range applied {
		if version < 1 {
			return fmt.Errorf("localstore: gateway migration ledger has invalid version %d", version)
		}
		if version > supportedVersion {
			return fmt.Errorf("localstore: gateway schema version %d is newer than supported version %d", version, supportedVersion)
		}
		maxApplied = max(maxApplied, version)
	}
	for version := 1; version <= maxApplied; version++ {
		if !applied[version] {
			return fmt.Errorf("localstore: gateway migration ledger has a gap at version %d", version)
		}
	}
	return nil
}

func validateGatewayMigrationSet(migrations []gatewayMigration) error {
	if len(migrations) == 0 {
		return fmt.Errorf("localstore: gateway migration set is empty")
	}
	for index, migration := range migrations {
		if migration.version != index+1 {
			return fmt.Errorf("localstore: gateway migrations are not contiguous at version %d", index+1)
		}
	}
	return nil
}

func loadGatewayMigrations() ([]gatewayMigration, error) {
	entries, err := gatewayMigrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("localstore: read embedded gateway migrations: %w", err)
	}
	migrations := make([]gatewayMigration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("localstore: invalid gateway migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("localstore: parse gateway migration %q: %w", entry.Name(), err)
		}
		contents, err := gatewayMigrationFiles.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("localstore: read gateway migration %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, gatewayMigration{version: version, name: entry.Name(), sql: string(contents)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	if len(migrations) != GatewaySchemaVersion {
		return nil, fmt.Errorf("localstore: embedded gateway migration count %d does not match schema version %d", len(migrations), GatewaySchemaVersion)
	}
	if err := validateGatewayMigrationSet(migrations); err != nil {
		return nil, err
	}
	return migrations, nil
}

func validateGatewayMigrationLedger(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `PRAGMA table_info(gateway_schema_migrations)`)
	if err != nil {
		return fmt.Errorf("localstore: inspect gateway migration ledger shape: %w", err)
	}
	defer rows.Close()
	type column struct {
		name         string
		columnType   string
		notNull      int
		defaultValue any
		primaryKey   int
	}
	columns := make([]column, 0, 2)
	for rows.Next() {
		var cid int
		var value column
		if err := rows.Scan(&cid, &value.name, &value.columnType, &value.notNull, &value.defaultValue, &value.primaryKey); err != nil {
			return fmt.Errorf("localstore: scan gateway migration ledger shape: %w", err)
		}
		columns = append(columns, value)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("localstore: read gateway migration ledger shape: %w", err)
	}
	valid := len(columns) == 2 &&
		columns[0].name == "version" && strings.EqualFold(columns[0].columnType, "INTEGER") && columns[0].primaryKey == 1 &&
		columns[1].name == "applied_at" && strings.EqualFold(columns[1].columnType, "TIMESTAMP") && columns[1].notNull == 1 && columns[1].primaryKey == 0
	if valid {
		defaultValue, ok := columns[1].defaultValue.(string)
		valid = ok && strings.EqualFold(strings.Trim(defaultValue, "()"), "CURRENT_TIMESTAMP")
	}
	if !valid {
		return fmt.Errorf("localstore: gateway migration ledger shape is invalid")
	}
	return nil
}

func gatewayAppliedVersions(ctx context.Context, conn *sql.Conn) (map[int]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT version FROM gateway_schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("localstore: read gateway migration ledger: %w", err)
	}
	defer rows.Close()
	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("localstore: scan gateway migration ledger: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("localstore: iterate gateway migration ledger: %w", err)
	}
	return applied, nil
}
