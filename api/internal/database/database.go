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

// Package database opens the API server's own database: it creates the database when missing and applies the
// versioned migrations embedded in the binary.
package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/wso2/integration-workflow-runtime/api/internal/config"
)

const (
	connectTimeout  = 10 * time.Second
	connMaxLifetime = time.Hour
	// PostgreSQL's maintenance database, there on every server; the API database is created from it.
	postgresAdminDatabase = "postgres"
	// SQLSTATEs a concurrent CREATE DATABASE from another node can return.
	pgDuplicateDatabase = "42P04"
	pgUniqueViolation   = "23505"
)

// Open creates the API server's database when it is missing, migrates it to the latest version and returns a pool.
func Open(ctx context.Context, cfg config.Database, logger *slog.Logger) (*sql.DB, error) {
	d, err := newDialect(cfg)
	if err != nil {
		return nil, err
	}
	if err := d.ensureDatabase(ctx); err != nil {
		return nil, fmt.Errorf("unable to create database %s: %w", cfg.Name, err)
	}
	db, err := d.open(cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("unable to open database %s: %w", cfg.Name, err)
	}
	configurePool(db, cfg)
	if err := migrate(ctx, db, d, logger); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("unable to migrate database %s: %w", cfg.Name, err)
	}
	return db, nil
}

func configurePool(db *sql.DB, cfg config.Database) {
	db.SetMaxOpenConns(cfg.MaxConns)
	db.SetMaxIdleConns(cfg.MaxConns)
	// An in-memory SQLite database lives only as long as its connection.
	if !cfg.InMemory {
		db.SetConnMaxLifetime(connMaxLifetime)
	}
}

// dialect holds what differs between the supported databases.
type dialect struct {
	cfg  config.Database
	tls  *tls.Config
	open func(name string) (*sql.DB, error)
	// ensureDatabase creates the database when it is missing.
	ensureDatabase func(ctx context.Context) error
	// lock serializes migrations across nodes; the returned function releases the lock.
	lock             func(ctx context.Context, conn *sql.Conn) (func(), error)
	tableExistsQuery string
	insertVersion    string
	// MySQL commits DDL implicitly, so its migrations cannot run in a transaction.
	transactionalDDL bool
}

func newDialect(cfg config.Database) (*dialect, error) {
	tlsConfig, err := clientTLS(cfg.TLS)
	if err != nil {
		return nil, err
	}
	d := &dialect{cfg: cfg, tls: tlsConfig}
	switch cfg.Type {
	case config.PostgreSQL:
		d.open = d.openPostgreSQL
		d.ensureDatabase = d.ensurePostgreSQL
		d.lock = lockPostgreSQL
		d.tableExistsQuery = "SELECT COUNT(*) FROM information_schema.tables " +
			"WHERE table_schema = current_schema() AND table_name = $1"
		d.insertVersion = "INSERT INTO schema_version (version, description, applied_at) VALUES ($1, $2, $3)"
		d.transactionalDDL = true
	case config.MySQL:
		d.open = d.openMySQL
		d.ensureDatabase = d.ensureMySQL
		d.lock = func(ctx context.Context, conn *sql.Conn) (func(), error) { return lockMySQL(ctx, conn, cfg.Name) }
		d.tableExistsQuery = "SELECT COUNT(*) FROM information_schema.tables " +
			"WHERE table_schema = DATABASE() AND table_name = ?"
		d.insertVersion = "INSERT INTO schema_version (version, description, applied_at) VALUES (?, ?, ?)"
	case config.SQLite:
		d.open = d.openSQLite
		d.ensureDatabase = d.ensureSQLite
		// One process owns a SQLite database, so there is nobody to serialize with.
		d.lock = func(context.Context, *sql.Conn) (func(), error) { return func() {}, nil }
		d.tableExistsQuery = "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
		d.insertVersion = "INSERT INTO schema_version (version, description, applied_at) VALUES (?, ?, ?)"
		d.transactionalDDL = true
	default:
		return nil, fmt.Errorf("unsupported database type %q", cfg.Type)
	}
	return d, nil
}

func (d *dialect) openPostgreSQL(name string) (*sql.DB, error) {
	conf, err := pgx.ParseConfig("")
	if err != nil {
		return nil, err
	}
	conf.Host = d.cfg.Host
	conf.Port = uint16(d.cfg.Port) //nolint:gosec // G115: the port was validated to be in range
	conf.User = d.cfg.User
	conf.Password = d.cfg.Password
	conf.Database = name
	conf.ConnectTimeout = connectTimeout
	conf.TLSConfig = d.tls
	conf.Fallbacks = nil
	return stdlib.OpenDB(*conf), nil
}

func (d *dialect) ensurePostgreSQL(ctx context.Context) error {
	admin, err := d.openPostgreSQL(postgresAdminDatabase)
	if err != nil {
		return err
	}
	defer func() { _ = admin.Close() }()
	var exists bool
	if err := admin.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)",
		d.cfg.Name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+d.cfg.Name+`"`)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == pgDuplicateDatabase || pgErr.Code == pgUniqueViolation) {
		return nil
	}
	return err
}

func lockPostgreSQL(ctx context.Context, conn *sql.Conn) (func(), error) {
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	if _, err := conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return nil, fmt.Errorf("unable to take the migration lock: %w", err)
	}
	return func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}, nil
}

func (d *dialect) mysqlConfig(name string) *mysql.Config {
	conf := mysql.NewConfig()
	conf.User = d.cfg.User
	conf.Passwd = d.cfg.Password
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort(d.cfg.Host, strconv.Itoa(d.cfg.Port))
	conf.DBName = name
	conf.TLS = d.tls
	conf.Timeout = connectTimeout
	conf.ParseTime = true
	return conf
}

func (d *dialect) openMySQL(name string) (*sql.DB, error) {
	connector, err := mysql.NewConnector(d.mysqlConfig(name))
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}

func (d *dialect) ensureMySQL(ctx context.Context) error {
	admin, err := d.openMySQL("")
	if err != nil {
		return err
	}
	defer func() { _ = admin.Close() }()
	var count int
	if err := admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?",
		d.cfg.Name).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err = admin.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS `"+d.cfg.Name+"`")
	return err
}

// MySQL's named locks are server-wide, so the name carries the database.
func lockMySQL(ctx context.Context, conn *sql.Conn, database string) (func(), error) {
	name := "workflow_api_migrate." + database
	var acquired sql.NullInt64
	err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", name, int(lockTimeout.Seconds())).Scan(&acquired)
	if err != nil {
		return nil, fmt.Errorf("unable to take the migration lock: %w", err)
	}
	if acquired.Int64 != 1 {
		return nil, fmt.Errorf("timed out after %s waiting for the migration lock %s", lockTimeout, name)
	}
	return func() {
		var released sql.NullInt64
		_ = conn.QueryRowContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", name).Scan(&released)
	}, nil
}

func (d *dialect) openSQLite(string) (*sql.DB, error) {
	if d.cfg.InMemory {
		return sql.Open("sqlite", ":memory:")
	}
	return sql.Open("sqlite", d.cfg.Path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)")
}

func (d *dialect) ensureSQLite(context.Context) error {
	if d.cfg.InMemory {
		return nil
	}
	return os.MkdirAll(filepath.Dir(d.cfg.Path), 0o750)
}

// clientTLS builds the database client's TLS configuration with Temporal's semantics: enableHostVerification off
// skips the host name check, but a configured CA still verifies the server's chain.
func clientTLS(t config.TLS) (*tls.Config, error) {
	if !t.Enabled {
		return nil, nil
	}
	conf := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.ServerName}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("unable to read database.tls.caFile: %w", err)
		}
		conf.RootCAs = x509.NewCertPool()
		if !conf.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("database.tls.caFile %s holds no PEM certificate", t.CAFile)
		}
	}
	if t.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("unable to load database.tls.certFile and keyFile: %w", err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}
	if !t.EnableHostVerification {
		conf.InsecureSkipVerify = true //nolint:gosec // G402: the operator turned host verification off, as in Temporal
		if conf.RootCAs != nil {
			conf.VerifyConnection = verifyChain(conf.RootCAs)
		}
	}
	return conf, nil
}

func verifyChain(roots *x509.CertPool) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("the database server sent no certificate")
		}
		intermediates := x509.NewCertPool()
		for _, cert := range state.PeerCertificates[1:] {
			intermediates.AddCert(cert)
		}
		_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
		return err
	}
}
