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

// Package cli implements the workflow-api command: the API server in front of the workflow engine.
package cli

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/wso2/integration-workflow-runtime/api/internal/config"
	"github.com/wso2/integration-workflow-runtime/api/internal/database"
	"github.com/wso2/integration-workflow-runtime/api/internal/engine"
	"github.com/wso2/integration-workflow-runtime/api/internal/server"
)

const usageText = `usage: workflow-api version
       workflow-api start --runtime-config <runtime.toml>`

type command struct {
	version string
	stdout  io.Writer
	stderr  io.Writer
	// stopContext returns the context that ends the server: SIGINT or SIGTERM in production.
	stopContext func() (context.Context, context.CancelFunc)
	listen      func(network, address string) (net.Listener, error)
}

// Main runs the command with the given arguments and returns the process exit code.
func Main(version string, args []string) int {
	c := command{
		version: version,
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		stopContext: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		},
		listen: net.Listen,
	}
	return c.run(args)
}

func (c command) run(args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(c.stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version":
		_, _ = fmt.Fprintf(c.stdout, "workflow-api %s\n", c.version)
		return 0
	case "start":
		if err := c.start(args[1:]); err != nil {
			_, _ = fmt.Fprintln(c.stderr, err)
			return 1
		}
		return 0
	default:
		_, _ = fmt.Fprintln(c.stderr, usageText)
		return 2
	}
}

func (c command) start(args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(c.stderr)
	runtimeConfig := flags.String("runtime-config", "", "the runtime's runtime.toml")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *runtimeConfig == "" {
		return errors.New("--runtime-config is required")
	}
	cfg, err := config.Load(*runtimeConfig)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(c.stderr, nil))
	ctx, stop := c.stopContext()
	defer stop()

	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	engineClient, err := engine.New(cfg.EngineAddress, logger)
	if err != nil {
		return err
	}
	defer engineClient.Close()

	listener, err := c.listen("tcp", cfg.Address())
	if err != nil {
		return fmt.Errorf("unable to listen on %s: %w", cfg.Address(), err)
	}
	logger.Info("api server started", "version", c.version, "address", listener.Addr().String(),
		"engine", cfg.EngineAddress, "database", cfg.Database.Type)
	return server.Serve(ctx, listener, server.Handler(readinessChecks(db, engineClient), logger), logger)
}

func readinessChecks(db *sql.DB, engineClient *engine.Client) []server.Check {
	return []server.Check{
		{Name: "database", Run: db.PingContext},
		{Name: "engine", Run: engineClient.Check},
	}
}
