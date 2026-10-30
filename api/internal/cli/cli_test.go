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

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

type harness struct {
	command
	stdout, stderr *bytes.Buffer
	stop           context.CancelFunc
	addresses      chan string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	h := &harness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, stop: stop, addresses: make(chan string, 1)}
	h.command = command{
		version:     "test",
		stdout:      h.stdout,
		stderr:      h.stderr,
		stopContext: func() (context.Context, context.CancelFunc) { return ctx, stop },
		// The configured address is replaced by a free port, reported to the test.
		listen: func(network, _ string) (net.Listener, error) {
			listener, err := net.Listen(network, "127.0.0.1:0")
			if err == nil {
				h.addresses <- listener.Addr().String()
			}
			return listener, err
		},
	}
	return h
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeFrontend(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	checker := health.NewServer()
	checker.SetServingStatus("temporal.api.workflowservice.v1.WorkflowService", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, checker)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	return listener.Addr().String()
}

func closedPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	return listener.Addr().String()
}

func readyz(t *testing.T, address string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+address+"/readyz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

// startServer runs `start` against a SQLite file and returns the address it serves on and its exit code channel.
func startServer(t *testing.T, h *harness, engineAddress string) (string, <-chan int) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "workflow.db")
	path := writeConfig(t, fmt.Sprintf("[api]\nengineAddress = %q\n[database]\ntype = \"sqlite\"\npath = %q\n",
		engineAddress, dbPath))
	exit := make(chan int, 1)
	go func() { exit <- h.run([]string{"start", "--runtime-config", path}) }()
	select {
	case address := <-h.addresses:
		return address, exit
	case code := <-exit:
		t.Fatalf("start exited with %d: %s", code, h.stderr)
	case <-time.After(30 * time.Second):
		t.Fatal("start did not listen")
	}
	return "", nil
}

func TestStartServesReadinessUntilStopped(t *testing.T) {
	h := newHarness(t)
	address, exit := startServer(t, h, fakeFrontend(t))
	if code, body := readyz(t, address); code != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("readyz = %d %v", code, body)
	}
	h.stop()
	if code := <-exit; code != 0 {
		t.Fatalf("exit = %d: %s", code, h.stderr)
	}
	if !strings.Contains(h.stderr.String(), "applied database migration") {
		t.Errorf("log lacks the migration: %s", h.stderr)
	}
}

func TestStartDoesNotWaitForTheEngine(t *testing.T) {
	h := newHarness(t)
	address, exit := startServer(t, h, closedPort(t))
	code, body := readyz(t, address)
	checks, _ := body["checks"].(map[string]any)
	if code != http.StatusServiceUnavailable || checks["engine"] != "failed" || checks["database"] != "ok" {
		t.Fatalf("readyz = %d %v", code, body)
	}
	h.stop()
	<-exit
}

func TestCommands(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"no command", nil, 2, "", "usage:"},
		{"unknown command", []string{"serve"}, 2, "", "usage:"},
		{"version", []string{"version"}, 0, "workflow-api test", ""},
		{"help", []string{"start", "-h"}, 0, "", "runtime-config"},
		{"bad flag", []string{"start", "--nope"}, 1, "", "flag provided but not defined"},
		{"missing config flag", []string{"start"}, 1, "", "--runtime-config is required"},
		{"missing file", []string{"start", "--runtime-config", "/nonexistent/runtime.toml"}, 1, "", "unable to read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if code := h.run(tt.args); code != tt.code {
				t.Fatalf("exit = %d, want %d (stderr %s)", code, tt.code, h.stderr)
			}
			if !strings.Contains(h.stdout.String(), tt.stdout) || !strings.Contains(h.stderr.String(), tt.stderr) {
				t.Fatalf("stdout %q, stderr %q", h.stdout, h.stderr)
			}
		})
	}
}

func TestStartFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	h := newHarness(t)
	_, port, _ := net.SplitHostPort(closedPort(t))
	path := writeConfig(t, "[database]\ntype = \"postgresql\"\nhost = \"127.0.0.1\"\nport = "+port+"\n")
	if code := h.run([]string{"start", "--runtime-config", path}); code != 1 ||
		!strings.Contains(h.stderr.String(), "unable to create database workflow_api") {
		t.Fatalf("exit = %d, stderr %s", code, h.stderr)
	}
}

func TestStartFailsWhenItCannotListen(t *testing.T) {
	h := newHarness(t)
	h.listen = func(string, string) (net.Listener, error) { return nil, errors.New("address in use") }
	path := writeConfig(t, "[database]\ntype = \"sqlite\"\ninMemory = true\n")
	if code := h.run([]string{"start", "--runtime-config", path}); code != 1 ||
		!strings.Contains(h.stderr.String(), "unable to listen on 127.0.0.1:9090") {
		t.Fatalf("exit = %d, stderr %s", code, h.stderr)
	}
}

func TestMainPrintsTheVersion(t *testing.T) {
	if code := Main("test", []string{"version"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
}
