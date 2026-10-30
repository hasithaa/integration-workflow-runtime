// Copyright (c) 2026, WSO2 LLC. (http://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	versionTable = "schema_version"
	lockTimeout  = 2 * time.Minute
	// An arbitrary constant naming the migration lock among PostgreSQL's advisory locks in this database.
	migrationLockKey int64 = 0x77666170695f6d67
)

//go:embed migrations
var migrationFiles embed.FS

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

type migration struct {
	version     int
	description string
	statements  []string
}

// loadMigrations returns a database type's migrations in version order, numbered from 1 without gaps.
func loadMigrations(files fs.FS, dbType string) ([]migration, error) {
	dir := path.Join("migrations", dbType)
	entries, err := fs.ReadDir(files, dir)
	if err != nil {
		return nil, err
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %s/%s is not named NNNN_description.sql", dir, entry.Name())
		}
		version, _ := strconv.Atoi(match[1])
		if version != len(migrations)+1 {
			return nil, fmt.Errorf("migration %s/%s: expected version %d", dir, entry.Name(), len(migrations)+1)
		}
		content, err := fs.ReadFile(files, path.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration{
			version:     version,
			description: strings.ReplaceAll(match[2], "_", " "),
			statements:  splitStatements(string(content)),
		})
	}
	return migrations, nil
}

// splitStatements splits a migration at semicolons that end a line, so no driver needs multi-statement support.
func splitStatements(content string) []string {
	var statements []string
	var current strings.Builder
	for line := range strings.Lines(content) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		if strings.HasSuffix(trimmed, ";") {
			statements = append(statements, strings.TrimSuffix(strings.TrimSpace(current.String()), ";"))
			current.Reset()
		}
	}
	if rest := strings.TrimSpace(current.String()); rest != "" {
		statements = append(statements, rest)
	}
	return statements
}

// migrate applies the pending migrations under the migration lock, each with its version record.
func migrate(ctx context.Context, db *sql.DB, d *dialect, logger *slog.Logger) error {
	migrations, err := loadMigrations(migrationFiles, d.cfg.Type)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	unlock, err := d.lock(ctx, conn)
	if err != nil {
		return err
	}
	defer unlock()

	current, err := currentVersion(ctx, conn, d)
	if err != nil {
		return err
	}
	latest := len(migrations)
	if current > latest {
		return fmt.Errorf("the database is at schema version %d, newer than this API server knows (%d)", current, latest)
	}
	for _, m := range migrations[current:] {
		if err := apply(ctx, conn, d, m); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.version, m.description, err)
		}
		logger.Info("applied database migration", "database", d.cfg.Name, "version", m.version,
			"description", m.description)
	}
	return nil
}

func currentVersion(ctx context.Context, conn *sql.Conn, d *dialect) (int, error) {
	var tables int
	if err := conn.QueryRowContext(ctx, d.tableExistsQuery, versionTable).Scan(&tables); err != nil {
		return 0, fmt.Errorf("unable to read the schema version: %w", err)
	}
	if tables == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT MAX(version) FROM "+versionTable).Scan(&version); err != nil {
		return 0, fmt.Errorf("unable to read the schema version: %w", err)
	}
	return int(version.Int64), nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func apply(ctx context.Context, conn *sql.Conn, d *dialect, m migration) error {
	if !d.transactionalDDL {
		return applyStatements(ctx, conn, d, m)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := applyStatements(ctx, tx, d, m); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func applyStatements(ctx context.Context, db execer, d *dialect, m migration) error {
	for _, statement := range m.statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err := db.ExecContext(ctx, d.insertVersion, m.version, m.description, time.Now().UTC())
	return err
}
