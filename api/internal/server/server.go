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

// Package server is the API server's HTTP server: the health endpoints today, the APIs and portals later.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	checkTimeout    = 3 * time.Second
	shutdownTimeout = 15 * time.Second
	statusOK        = "ok"
	statusFailed    = "failed"
)

// Check is one readiness condition, such as the database answering.
type Check struct {
	Name string
	Run  func(context.Context) error
}

type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Handler serves GET /healthz (the process is alive) and GET /readyz (every check passes).
func Handler(checks []Check, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": statusOK})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		result := runChecks(r.Context(), checks, logger)
		code := http.StatusOK
		if result.Status != "ready" {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, result)
	})
	return mux
}

// runChecks runs the checks concurrently; failures are logged in full but reported only by name.
func runChecks(ctx context.Context, checks []Check, logger *slog.Logger) readiness {
	result := readiness{Status: "ready", Checks: make(map[string]string, len(checks))}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, check := range checks {
		wg.Go(func() {
			checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			err := check.Run(checkCtx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				logger.Warn("readiness check failed", "check", check.Name, "error", err)
				result.Checks[check.Name] = statusFailed
				result.Status = "not ready"
				return
			}
			result.Checks[check.Name] = statusOK
		})
	}
	wg.Wait()
	return result
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// Serve serves handler on listener until ctx is done, then stops accepting requests and lets the ones in flight
// finish, for up to shutdownTimeout.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger) error {
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down the api server")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
