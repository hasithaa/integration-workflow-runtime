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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wso2/integration-workflow-runtime/api/internal/config"
)

const passwordEnv = "WORKFLOW_TEST_DB_PASSWORD"

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func versions(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT version FROM schema_version ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	return got
}

func latestVersion(t *testing.T, dbType string) int {
	t.Helper()
	migrations, err := loadMigrations(migrationFiles, dbType)
	if err != nil {
		t.Fatal(err)
	}
	return len(migrations)
}

// openTwice opens the database twice, as two starts would, and checks each migration is recorded once.
func openTwice(t *testing.T, cfg config.Database) {
	t.Helper()
	for i := range 2 {
		db, err := Open(t.Context(), cfg, quietLogger())
		if err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
		got := versions(t, db)
		if len(got) != latestVersion(t, cfg.Type) || got[0] != 1 {
			t.Fatalf("start %d: versions = %v", i+1, got)
		}
		if err := db.PingContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
	}
}

func TestSQLiteFileIsCreatedAndMigratedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "workflow_api.db")
	openTwice(t, config.Database{Type: config.SQLite, Name: "workflow_api", Path: path, MaxConns: 1})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file: %v", err)
	}
}

func TestSQLiteInMemory(t *testing.T) {
	db, err := Open(t.Context(), config.Database{Type: config.SQLite, Name: "workflow_api", InMemory: true, MaxConns: 1},
		quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if got := versions(t, db); len(got) != latestVersion(t, config.SQLite) {
		t.Fatalf("versions = %v", got)
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow_api.db")
	cfg := config.Database{Type: config.SQLite, Name: "workflow_api", Path: path, MaxConns: 1}
	db, err := Open(t.Context(), cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(),
		"INSERT INTO schema_version (version, description, applied_at) VALUES (999, 'future', ?)", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(t.Context(), cfg, quietLogger()); err == nil || !strings.Contains(err.Error(), "newer than") {
		t.Fatalf("Open = %v, want a newer-schema error", err)
	}
}

func TestFailedMigrationIsRolledBack(t *testing.T) {
	cfg := config.Database{Type: config.SQLite, Name: "workflow_api", InMemory: true, MaxConns: 1}
	d, err := newDialect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db, err := d.open(cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	configurePool(db, cfg)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	bad := migration{version: 1, description: "bad", statements: []string{"CREATE TABLE t (x INTEGER)", "NOT SQL"}}
	if err := apply(t.Context(), conn, d, bad); err == nil {
		t.Fatal("apply of an invalid statement succeeded")
	}
	var tables int
	if err := conn.QueryRowContext(t.Context(), d.tableExistsQuery, "t").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("table t after rollback: count %d, err %v", tables, err)
	}
}

func TestLoadMigrations(t *testing.T) {
	for _, dbType := range []string{config.PostgreSQL, config.MySQL, config.SQLite} {
		migrations, err := loadMigrations(migrationFiles, dbType)
		if err != nil {
			t.Fatalf("%s: %v", dbType, err)
		}
		if len(migrations) == 0 || migrations[0].description != "create schema version" {
			t.Fatalf("%s: migrations = %+v", dbType, migrations)
		}
	}
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{"bad name", fstest.MapFS{"migrations/x/1_a.sql": {}}, "NNNN_description.sql"},
		{"gap", fstest.MapFS{"migrations/x/0001_a.sql": {}, "migrations/x/0003_b.sql": {}}, "expected version 2"},
		{"missing directory", fstest.MapFS{"migrations/y/0001_a.sql": {}}, "does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := loadMigrations(tt.files, "x"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadMigrations = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSplitStatements(t *testing.T) {
	got := splitStatements("-- header; not a statement\n\nCREATE TABLE a (\n  x INT\n);\n" +
		"INSERT INTO a VALUES (1);\nSELECT 1")
	want := []string{"CREATE TABLE a (\n  x INT\n)", "INSERT INTO a VALUES (1)", "SELECT 1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}
}

func TestUnsupportedType(t *testing.T) {
	if _, err := Open(t.Context(), config.Database{Type: "oracle"}, quietLogger()); err == nil {
		t.Fatal("Open of an unsupported type succeeded")
	}
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// selfSigned writes a self-signed certificate and its key and returns the certificate.
func selfSigned(t *testing.T, dir string) (*x509.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "db"}, DNSNames: []string{"db"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certFile, keyFile
}

func TestClientTLS(t *testing.T) {
	dir := t.TempDir()
	cert, certFile, keyFile := selfSigned(t, dir)

	if conf, err := clientTLS(config.TLS{}); conf != nil || err != nil {
		t.Fatalf("disabled TLS = %v, %v", conf, err)
	}
	conf, err := clientTLS(config.TLS{Enabled: true, CAFile: certFile, CertFile: certFile, KeyFile: keyFile,
		ServerName: "db", EnableHostVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	if conf.InsecureSkipVerify || conf.RootCAs == nil || len(conf.Certificates) != 1 || conf.ServerName != "db" {
		t.Fatalf("verified TLS = %+v", conf)
	}

	conf, err = clientTLS(config.TLS{Enabled: true, CAFile: certFile})
	if err != nil {
		t.Fatal(err)
	}
	if !conf.InsecureSkipVerify || conf.VerifyConnection == nil {
		t.Fatal("without host verification the chain must still be checked against the CA")
	}
	if err := conf.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err != nil {
		t.Fatalf("chain from the CA rejected: %v", err)
	}
	other, _, _ := selfSigned(t, t.TempDir())
	if err := conf.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{other}}); err == nil {
		t.Fatal("certificate from another CA accepted")
	}
	if err := conf.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("no certificate accepted")
	}

	notPEM := filepath.Join(dir, "x.pem")
	if err := os.WriteFile(notPEM, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []config.TLS{
		{Enabled: true, CAFile: filepath.Join(dir, "absent.pem")},
		{Enabled: true, CAFile: notPEM},
		{Enabled: true, CertFile: certFile, KeyFile: notPEM},
	} {
		if _, err := clientTLS(bad); err == nil {
			t.Errorf("clientTLS(%+v) succeeded", bad)
		}
	}
}

// serverDatabase returns the shared test server's connection, or skips when WORKFLOW_TEST_DB_PASSWORD is not set.
func serverDatabase(t *testing.T, dbType string) config.Database {
	t.Helper()
	password, ok := os.LookupEnv(passwordEnv)
	if !ok {
		t.Skipf("%s is not set; start the test databases to run this test", passwordEnv)
	}
	cfg := config.Database{Type: dbType, Host: "127.0.0.1", Password: password, MaxConns: 4,
		Name: fmt.Sprintf("api_test_%d", time.Now().UnixNano())}
	switch dbType {
	case config.PostgreSQL:
		cfg.Port, cfg.User = 15432, "workflow"
	case config.MySQL:
		cfg.Port, cfg.User = 13306, "root"
	}
	t.Cleanup(func() { dropDatabase(t, cfg) })
	return cfg
}

func dropDatabase(t *testing.T, cfg config.Database) {
	d, err := newDialect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	admin := ""
	if cfg.Type == config.PostgreSQL {
		admin = postgresAdminDatabase
	}
	db, err := d.open(admin)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	quote := map[string]string{config.PostgreSQL: `"`, config.MySQL: "`"}[cfg.Type]
	if _, err := db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+quote+cfg.Name+quote); err != nil {
		t.Errorf("drop %s: %v", cfg.Name, err)
	}
}

func TestServerDatabases(t *testing.T) {
	for _, dbType := range []string{config.PostgreSQL, config.MySQL} {
		t.Run(dbType, func(t *testing.T) {
			openTwice(t, serverDatabase(t, dbType))
		})
	}
}

// Nodes starting together all create and migrate the same database; the lock lets one apply each migration.
func TestConcurrentStartsMigrateOnce(t *testing.T) {
	for _, dbType := range []string{config.PostgreSQL, config.MySQL} {
		t.Run(dbType, func(t *testing.T) {
			cfg := serverDatabase(t, dbType)
			var wg sync.WaitGroup
			errs := make(chan error, 4)
			for range 4 {
				wg.Go(func() {
					db, err := Open(t.Context(), cfg, quietLogger())
					if err == nil {
						_ = db.Close()
					}
					errs <- err
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			openTwice(t, cfg)
		})
	}
}

func TestLockIsHeldOnTheServer(t *testing.T) {
	for _, dbType := range []string{config.PostgreSQL, config.MySQL} {
		t.Run(dbType, func(t *testing.T) {
			cfg := serverDatabase(t, dbType)
			db, err := Open(t.Context(), cfg, quietLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			d, _ := newDialect(cfg)
			holder, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Close() }()
			unlock, err := d.lock(t.Context(), holder)
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			waiter, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = waiter.Close() }()
			var got sql.NullInt64
			if dbType == config.PostgreSQL {
				var taken bool
				err = waiter.QueryRowContext(t.Context(), "SELECT pg_try_advisory_lock($1)", migrationLockKey).Scan(&taken)
				if taken {
					got.Int64 = 1
				}
			} else {
				err = waiter.QueryRowContext(t.Context(), "SELECT GET_LOCK(?, 0)", "workflow_api_migrate."+cfg.Name).
					Scan(&got)
			}
			if err != nil || got.Int64 == 1 {
				t.Fatalf("a second session took the migration lock: %v, %v", got, err)
			}
		})
	}
}

func TestServerDialectsBuildTheirConnections(t *testing.T) {
	cfg := config.Database{Type: config.MySQL, Host: "db", Port: 3307, User: "u", Password: "p", Name: "wf_api",
		TLS: config.TLS{Enabled: true, EnableHostVerification: true}}
	d, err := newDialect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	conf := d.mysqlConfig("wf_api")
	if conf.Addr != "db:3307" || conf.DBName != "wf_api" || conf.User != "u" || conf.TLS == nil || !conf.ParseTime {
		t.Fatalf("mysql config = %+v", conf)
	}
	if d.transactionalDDL {
		t.Error("mysql migrations cannot run in a transaction")
	}
	cfg.Type, cfg.Port = config.PostgreSQL, 5433
	if d, err = newDialect(cfg); err != nil || !d.transactionalDDL {
		t.Fatalf("postgresql dialect = %+v, %v", d, err)
	}
	for _, dbType := range []string{config.PostgreSQL, config.MySQL} {
		cfg.Type = dbType
		d, _ := newDialect(cfg)
		db, err := d.open("wf_api")
		if err != nil {
			t.Fatalf("%s: %v", dbType, err)
		}
		_ = db.Close()
	}
	cfg.TLS.CAFile = filepath.Join(t.TempDir(), "absent.pem")
	if _, err := newDialect(cfg); err == nil {
		t.Fatal("a missing CA file was accepted")
	}
}
