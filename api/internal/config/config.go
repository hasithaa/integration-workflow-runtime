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

// Package config reads the API server's settings from the runtime's conf/runtime.toml.
//
// It owns the [api] table, shares [database] with the engine, and reads only the [engine] keys that locate this
// node's frontend; other tables belong to other components.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// Database types.
const (
	PostgreSQL = "postgresql"
	MySQL      = "mysql"
	SQLite     = "sqlite"
)

const (
	defaultPort            = 9090
	defaultEnginePort      = 7233
	defaultAPIDatabaseName = "workflow_api"
	loopback               = "127.0.0.1"
	anyAddress             = "0.0.0.0"
)

var (
	defaultDBPorts = map[string]int{PostgreSQL: 5432, MySQL: 3306}
	// The name becomes a database or a file name, so it is kept to characters every database accepts unquoted.
	databaseNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
)

// Config is the API server's validated configuration.
type Config struct {
	BindAddress   string
	Port          int
	EngineAddress string
	Database      Database
}

// Database is the connection to the API server's own database.
type Database struct {
	Type     string
	Host     string
	Port     int
	User     string
	Password string
	// Name is the database on PostgreSQL and MySQL; on SQLite it names the file next to database.path.
	Name     string
	Path     string
	InMemory bool
	MaxConns int
	TLS      TLS
}

// TLS is the TLS configuration for the database connection.
type TLS struct {
	Enabled                bool
	CAFile                 string
	CertFile               string
	KeyFile                string
	ServerName             string
	EnableHostVerification bool
}

// Address is the host:port the HTTP server listens on.
func (c *Config) Address() string {
	return net.JoinHostPort(c.BindAddress, strconv.Itoa(c.Port))
}

type apiSettings struct {
	BindAddress   string `toml:"bindAddress"`
	Port          int    `toml:"port"`
	EngineAddress string `toml:"engineAddress"`
}

type engineSettings struct {
	BindAddress      string `toml:"bindAddress"`
	BroadcastAddress string `toml:"broadcastAddress"`
	Ports            struct {
		Frontend int `toml:"frontend"`
	} `toml:"ports"`
}

// databaseSettings lists every [database] key the runtime knows, so the keys only the engine uses are accepted.
type databaseSettings struct {
	Type            string      `toml:"type"`
	Host            string      `toml:"host"`
	Port            int         `toml:"port"`
	User            string      `toml:"user"`
	Password        string      `toml:"password"`
	Name            string      `toml:"name"`
	VisibilityName  string      `toml:"visibilityName"`
	APIDatabaseName string      `toml:"apiDatabaseName"`
	Path            string      `toml:"path"`
	InMemory        bool        `toml:"inMemory"`
	MaxConns        int         `toml:"maxConns"`
	TLS             tlsSettings `toml:"tls"`
}

type tlsSettings struct {
	Enabled                bool   `toml:"enabled"`
	CAFile                 string `toml:"caFile"`
	CertFile               string `toml:"certFile"`
	KeyFile                string `toml:"keyFile"`
	ServerName             string `toml:"serverName"`
	EnableHostVerification bool   `toml:"enableHostVerification"`
}

type settings struct {
	API      apiSettings
	Engine   engineSettings
	Database databaseSettings
}

// Load reads a runtime.toml file and returns the API server's validated configuration.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(filepath.Clean(path)) //nolint:gosec // G304: the path is the operator's --runtime-config flag
	if err != nil {
		return nil, fmt.Errorf("unable to read %s: %w", path, err)
	}
	return parse(data, os.LookupEnv)
}

func parse(data []byte, lookupEnv func(string) (string, bool)) (*Config, error) {
	values, err := toml.Parser().Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime configuration: %w", err)
	}
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(values, ""), nil); err != nil {
		return nil, fmt.Errorf("invalid runtime configuration: %w", err)
	}
	var s settings
	if err := decodeTable(k, "api", &s.API, true); err != nil {
		return nil, err
	}
	// The engine validates [engine]; only the keys that locate its frontend are read here.
	if err := decodeTable(k, "engine", &s.Engine, false); err != nil {
		return nil, err
	}
	if err := decodeTable(k, "database", &s.Database, true); err != nil {
		return nil, err
	}
	s.applyDefaults()
	if err := s.validate(); err != nil {
		return nil, err
	}
	password, err := resolveSecret(s.Database.Password, lookupEnv)
	if err != nil {
		return nil, err
	}
	return s.config(password), nil
}

// decodeTable fills target from one table; strict rejects keys the target does not know (typos).
func decodeTable(k *koanf.Koanf, table string, target any, strict bool) error {
	var meta mapstructure.Metadata
	conf := koanf.UnmarshalConf{Tag: "toml", DecoderConfig: &mapstructure.DecoderConfig{Metadata: &meta, Result: target}}
	if err := k.UnmarshalWithConf(table, target, conf); err != nil {
		return fmt.Errorf("invalid runtime configuration in [%s]: %w", table, err)
	}
	if !strict || len(meta.Unused) == 0 {
		return nil
	}
	unknown := make([]string, len(meta.Unused))
	for i, key := range meta.Unused {
		unknown[i] = table + "." + key
	}
	slices.Sort(unknown)
	return fmt.Errorf("unknown runtime configuration keys: %s", strings.Join(unknown, ", "))
}

func (s *settings) applyDefaults() {
	a, e, d := &s.API, s.Engine, &s.Database
	a.BindAddress = withDefault(a.BindAddress, loopback)
	a.Port = withDefault(a.Port, defaultPort)
	if a.EngineAddress == "" {
		a.EngineAddress = net.JoinHostPort(engineHost(e), strconv.Itoa(withDefault(e.Ports.Frontend, defaultEnginePort)))
	}
	d.APIDatabaseName = withDefault(d.APIDatabaseName, defaultAPIDatabaseName)
	if d.Type == SQLite {
		d.MaxConns = withDefault(d.MaxConns, 1)
		return
	}
	d.Port = withDefault(d.Port, defaultDBPorts[d.Type])
	d.Name = withDefault(d.Name, "temporal")
	d.VisibilityName = withDefault(d.VisibilityName, "temporal_visibility")
	d.MaxConns = withDefault(d.MaxConns, 20)
}

// engineHost is the address this node's frontend is reached on, as the engine itself advertises it.
func engineHost(e engineSettings) string {
	switch {
	case e.BroadcastAddress != "":
		return e.BroadcastAddress
	case e.BindAddress != "" && e.BindAddress != anyAddress:
		return e.BindAddress
	default:
		return loopback
	}
}

func (s *settings) validate() error {
	var problems []string
	a, d := s.API, s.Database
	if net.ParseIP(a.BindAddress) == nil {
		problems = append(problems, fmt.Sprintf("api.bindAddress %q must be an IP address", a.BindAddress))
	}
	if !validPort(a.Port) {
		problems = append(problems, fmt.Sprintf("api.port %d is not a valid port", a.Port))
	}
	if host, port, err := net.SplitHostPort(a.EngineAddress); err != nil || host == "" || !validPortString(port) {
		problems = append(problems, fmt.Sprintf("api.engineAddress %q must be host:port", a.EngineAddress))
	}
	if !databaseNamePattern.MatchString(d.APIDatabaseName) {
		problems = append(problems, fmt.Sprintf("database.apiDatabaseName %q must be letters, digits and underscores "+
			"(at most 63), not starting with a digit", d.APIDatabaseName))
	}
	if d.MaxConns < 1 {
		problems = append(problems, "database.maxConns must be at least 1")
	}
	switch d.Type {
	case SQLite:
		if (d.Path == "") == !d.InMemory {
			problems = append(problems, "a sqlite database needs exactly one of database.path and database.inMemory = true")
		}
		if d.Path != "" && filepath.Clean(d.Path) == sqlitePath(d) {
			problems = append(problems, "database.apiDatabaseName must name a file other than database.path")
		}
	case PostgreSQL, MySQL:
		if d.Host == "" {
			problems = append(problems, "database.host is required")
		}
		if !validPort(d.Port) {
			problems = append(problems, fmt.Sprintf("database.port %d is not a valid port", d.Port))
		}
		if d.APIDatabaseName == d.Name || d.APIDatabaseName == d.VisibilityName {
			problems = append(problems, "database.apiDatabaseName must differ from database.name and database.visibilityName")
		}
	default:
		problems = append(problems, fmt.Sprintf("database.type %q must be one of postgresql, mysql, sqlite", d.Type))
	}
	if !d.TLS.Enabled && (d.TLS.CAFile != "" || d.TLS.CertFile != "" || d.TLS.KeyFile != "") {
		problems = append(problems, "database.tls files are set but database.tls.enabled is false")
	}
	if (d.TLS.CertFile == "") != (d.TLS.KeyFile == "") {
		problems = append(problems, "database.tls.certFile and database.tls.keyFile must be set together")
	}
	if len(problems) > 0 {
		return errors.New("invalid runtime configuration: " + strings.Join(problems, "; "))
	}
	return nil
}

// sqlitePath is the API server's SQLite file: <apiDatabaseName>.db next to the engine's database.path.
func sqlitePath(d databaseSettings) string {
	return filepath.Join(filepath.Dir(d.Path), d.APIDatabaseName+".db")
}

// resolveSecret returns a literal value, or the variable named by $env{NAME}.
func resolveSecret(value string, lookupEnv func(string) (string, bool)) (string, error) {
	switch {
	case strings.HasPrefix(value, "$env{") && strings.HasSuffix(value, "}"):
		name := strings.TrimSuffix(strings.TrimPrefix(value, "$env{"), "}")
		resolved, ok := lookupEnv(name)
		if !ok {
			return "", fmt.Errorf("database.password refers to environment variable %s, which is not set", name)
		}
		return resolved, nil
	case strings.HasPrefix(value, "$secret{"):
		return "", errors.New("database.password: $secret{...} references are not supported yet; use $env{NAME}")
	default:
		return value, nil
	}
}

func (s *settings) config(password string) *Config {
	a, d := s.API, s.Database
	db := Database{
		Type:     d.Type,
		Host:     d.Host,
		Port:     d.Port,
		User:     d.User,
		Password: password,
		Name:     d.APIDatabaseName,
		InMemory: d.InMemory,
		MaxConns: d.MaxConns,
		TLS: TLS{
			Enabled:                d.TLS.Enabled,
			CAFile:                 d.TLS.CAFile,
			CertFile:               d.TLS.CertFile,
			KeyFile:                d.TLS.KeyFile,
			ServerName:             d.TLS.ServerName,
			EnableHostVerification: d.TLS.EnableHostVerification,
		},
	}
	if d.Type == SQLite && !d.InMemory {
		db.Path = sqlitePath(d)
	}
	return &Config{BindAddress: a.BindAddress, Port: a.Port, EngineAddress: a.EngineAddress, Database: db}
}

func validPort(port int) bool { return port > 0 && port < 65536 }

func validPortString(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && validPort(n)
}

func withDefault[T comparable](value, fallback T) T {
	var zero T
	if value == zero {
		return fallback
	}
	return value
}
