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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func pass(context.Context) error { return nil }

func fail(context.Context) error { return errors.New("connection refused") }

func get(t *testing.T, h http.Handler, method, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	var body map[string]any
	if rec.Body.Len() > 0 && rec.Header().Get("Content-Type") == "application/json" {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return rec.Code, body
}

func TestHealthz(t *testing.T) {
	code, body := get(t, Handler([]Check{{"database", fail}}, quietLogger()), http.MethodGet, "/healthz")
	if code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz = %d %v", code, body)
	}
}

func TestReadyzNamesTheFailedCheck(t *testing.T) {
	tests := []struct {
		name   string
		checks []Check
		code   int
		status string
		want   map[string]any
	}{
		{"all pass", []Check{{"database", pass}, {"engine", pass}}, http.StatusOK, "ready",
			map[string]any{"database": "ok", "engine": "ok"}},
		{"engine down", []Check{{"database", pass}, {"engine", fail}}, http.StatusServiceUnavailable, "not ready",
			map[string]any{"database": "ok", "engine": "failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, body := get(t, Handler(tt.checks, quietLogger()), http.MethodGet, "/readyz")
			if code != tt.code || body["status"] != tt.status {
				t.Fatalf("readyz = %d %v", code, body)
			}
			checks, _ := body["checks"].(map[string]any)
			for name, want := range tt.want {
				if checks[name] != want {
					t.Errorf("checks[%s] = %v, want %v", name, checks[name], want)
				}
			}
		})
	}
}

func TestReadyzBoundsSlowChecks(t *testing.T) {
	slow := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	start := time.Now()
	code, _ := get(t, Handler([]Check{{"engine", slow}}, quietLogger()), http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable || time.Since(start) > checkTimeout+time.Second {
		t.Fatalf("readyz = %d after %s", code, time.Since(start))
	}
}

func TestOnlyGetIsServed(t *testing.T) {
	h := Handler(nil, quietLogger())
	if code, _ := get(t, h, http.MethodPost, "/healthz"); code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz = %d", code)
	}
	if code, _ := get(t, h, http.MethodGet, "/nope"); code != http.StatusNotFound {
		t.Errorf("GET /nope = %d", code)
	}
}

func TestServeStopsGracefully(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, listener, Handler(nil, quietLogger()), quietLogger()) }()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve = %v, want a clean stop", err)
	}
}

func TestServeReportsAListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	if err := Serve(t.Context(), listener, Handler(nil, quietLogger()), quietLogger()); err == nil {
		t.Fatal("Serve on a closed listener succeeded")
	}
}
