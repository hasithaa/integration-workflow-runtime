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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func mustParse(t *testing.T, toml string) *Config {
	t.Helper()
	cfg, err := parse([]byte(toml), noEnv)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

func TestDefaults(t *testing.T) {
	cfg := mustParse(t, "[database]\ntype = \"postgresql\"\nhost = \"db\"\nuser = \"wf\"\npassword = \"pw\"\n")
	if cfg.Address() != "127.0.0.1:9090" || cfg.EngineAddress != "127.0.0.1:7233" {
		t.Errorf("address = %q, engine = %q", cfg.Address(), cfg.EngineAddress)
	}
	want := Database{Type: PostgreSQL, Host: "db", Port: 5432, User: "wf", Password: "pw", Name: "workflow_api",
		MaxConns: 20}
	if cfg.Database != want {
		t.Errorf("database = %+v, want %+v", cfg.Database, want)
	}
	if got := mustParse(t, "[database]\ntype = \"mysql\"\nhost = \"db\"\n").Database.Port; got != 3306 {
		t.Errorf("mysql port = %d, want 3306", got)
	}
}

func TestEngineAddressFollowsTheEngine(t *testing.T) {
	tests := []struct{ name, engine, want string }{
		{"broadcast address wins", "[engine]\nbindAddress = \"0.0.0.0\"\nbroadcastAddress = \"10.0.0.5\"\n", "10.0.0.5:7233"},
		{"bind address", "[engine]\nbindAddress = \"10.0.0.6\"\n", "10.0.0.6:7233"},
		{"any address falls back to loopback", "[engine]\nbindAddress = \"0.0.0.0\"\n", "127.0.0.1:7233"},
		{"frontend port", "[engine.ports]\nfrontend = 17233\n", "127.0.0.1:17233"},
		{"explicit", "[api]\nengineAddress = \"engine.internal:7233\"\n[engine]\nbindAddress = \"10.0.0.6\"\n",
			"engine.internal:7233"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, tt.engine+"[database]\ntype = \"sqlite\"\ninMemory = true\n")
			if cfg.EngineAddress != tt.want {
				t.Errorf("engineAddress = %q, want %q", cfg.EngineAddress, tt.want)
			}
		})
	}
}

func TestEngineKeysAreLeftToTheEngine(t *testing.T) {
	mustParse(t, `
[runtime]
profiles = ["all"]

[engine]
logLevel = "warn"
historyShards = 4
futureKey = true

[engine.ports]
history = 17234

[users.sso]
issuer = "https://idp.example.com"

[database]
type = "sqlite"
inMemory = true
`)
}

func TestAPISettingsOverrideDefaults(t *testing.T) {
	cfg := mustParse(t, `
[api]
bindAddress = "0.0.0.0"
port = 19090

[database]
type = "mysql"
host = "db"
port = 3307
name = "t"
visibilityName = "v"
apiDatabaseName = "wf_api"
maxConns = 5

[database.tls]
enabled = true
caFile = "/certs/ca.pem"
certFile = "/certs/client.pem"
keyFile = "/certs/client.key"
serverName = "db.internal"
enableHostVerification = true
`)
	if cfg.Address() != "0.0.0.0:19090" {
		t.Errorf("address = %q", cfg.Address())
	}
	d := cfg.Database
	if d.Name != "wf_api" || d.Port != 3307 || d.MaxConns != 5 {
		t.Errorf("database = %+v", d)
	}
	want := TLS{true, "/certs/ca.pem", "/certs/client.pem", "/certs/client.key", "db.internal", true}
	if d.TLS != want {
		t.Errorf("tls = %+v, want %+v", d.TLS, want)
	}
}

func TestSQLiteKeepsTheAPIDatabaseInItsOwnFile(t *testing.T) {
	cfg := mustParse(t, "[database]\ntype = \"sqlite\"\npath = \"data/workflow.db\"\n")
	if want := filepath.Join("data", "workflow_api.db"); cfg.Database.Path != want || cfg.Database.MaxConns != 1 {
		t.Errorf("database = %+v, want path %q", cfg.Database, want)
	}
	cfg = mustParse(t, "[database]\ntype = \"sqlite\"\ninMemory = true\napiDatabaseName = \"other\"\n")
	if cfg.Database.Path != "" || !cfg.Database.InMemory || cfg.Database.Name != "other" {
		t.Errorf("database = %+v", cfg.Database)
	}
}

func TestPasswordFromEnvironment(t *testing.T) {
	lookup := func(name string) (string, bool) { return "from-env", name == "WF_DB_PASSWORD" }
	toml := "[database]\ntype = \"postgresql\"\nhost = \"db\"\npassword = \"$env{WF_DB_PASSWORD}\"\n"
	cfg, err := parse([]byte(toml), lookup)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Password != "from-env" {
		t.Fatalf("password = %q, want the environment value", cfg.Database.Password)
	}
}

func TestInvalidConfigurationsAreRejected(t *testing.T) {
	const sqlite = "[database]\ntype = \"sqlite\"\ninMemory = true\n"
	const mysql = "[database]\ntype = \"mysql\"\nhost = \"db\"\n"
	tests := []struct{ name, toml, want string }{
		{"not toml", "[database\n", "invalid runtime configuration"},
		{"unknown database type", "[database]\ntype = \"oracle\"\n", `database.type "oracle"`},
		{"typo in api table", "[api]\nprot = 1\n" + sqlite, "api.prot"},
		{"typo in database table", sqlite + "hots = \"x\"\n", "database.hots"},
		{"wrong type in api table", "[api]\nport = \"x\"\n" + sqlite, "[api]"},
		{"wrong type in engine port", "[engine.ports]\nfrontend = \"x\"\n" + sqlite, "[engine]"},
		{"bind not an ip", "[api]\nbindAddress = \"localhost\"\n" + sqlite, "api.bindAddress"},
		{"bad port", "[api]\nport = 70000\n" + sqlite, "api.port 70000"},
		{"engine address without port", "[api]\nengineAddress = \"engine\"\n" + sqlite, "api.engineAddress"},
		{"engine address with bad port", "[api]\nengineAddress = \"engine:0\"\n" + sqlite, "api.engineAddress"},
		{"missing host", "[database]\ntype = \"postgresql\"\n", "database.host is required"},
		{"bad database port", mysql + "port = 70000\n", "database.port 70000"},
		{"clashes with name", mysql + "apiDatabaseName = \"temporal\"\n", "apiDatabaseName must differ"},
		{"clashes with visibility", mysql + "visibilityName = \"x\"\napiDatabaseName = \"x\"\n", "must differ"},
		{"unsafe name", mysql + "apiDatabaseName = \"api; drop\"\n", "letters, digits and underscores"},
		{"name too long", mysql + "apiDatabaseName = \"" + strings.Repeat("a", 64) + "\"\n", "at most 63"},
		{"sqlite file clash", "[database]\ntype = \"sqlite\"\npath = \"d/workflow_api.db\"\n", "other than database.path"},
		{"sqlite without location", "[database]\ntype = \"sqlite\"\n", "exactly one of database.path"},
		{"bad max conns", sqlite + "maxConns = -1\n", "maxConns must be at least 1"},
		{"tls files without tls", mysql + "[database.tls]\ncaFile = \"ca.pem\"\n", "tls.enabled is false"},
		{"cert without key", mysql + "[database.tls]\nenabled = true\ncertFile = \"c.pem\"\n", "set together"},
		{"missing env password", mysql + "password = \"$env{NOPE}\"\n", "NOPE, which is not set"},
		{"secret references", mysql + "password = \"$secret{db}\"\n", "not supported yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.toml), noEnv)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.toml")
	if err := os.WriteFile(path, []byte("[database]\ntype = \"sqlite\"\ninMemory = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load = %v", err)
	}
	_, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err == nil || !strings.Contains(err.Error(), "unable to read") {
		t.Fatalf("Load of a missing file = %v, want a read error", err)
	}
}

func TestDistributionConfigurationIsAccepted(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "distribution", "conf", "runtime.toml"))
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(string) (string, bool) { return "pw", true }
	if _, err := parse(data, lookup); err != nil {
		t.Fatalf("distribution runtime.toml: %v", err)
	}
}
