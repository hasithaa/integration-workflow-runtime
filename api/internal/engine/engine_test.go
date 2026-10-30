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

package engine

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

const workflowService = "temporal.api.workflowservice.v1.WorkflowService"

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeFrontend serves only gRPC health; the SDK treats the missing GetSystemInfo as an older server.
func fakeFrontend(t *testing.T, status healthpb.HealthCheckResponse_ServingStatus) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	checker := health.NewServer()
	checker.SetServingStatus(workflowService, status)
	healthpb.RegisterHealthServer(srv, checker)
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	return listener.Addr().String()
}

func newClient(t *testing.T, address string) *Client {
	t.Helper()
	c, err := New(address, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestCheckPassesWhenTheFrontendServes(t *testing.T) {
	if err := newClient(t, fakeFrontend(t, healthpb.HealthCheckResponse_SERVING)).Check(t.Context()); err != nil {
		t.Fatalf("Check = %v", err)
	}
}

func TestCheckFailsWhenTheFrontendIsNotServing(t *testing.T) {
	err := newClient(t, fakeFrontend(t, healthpb.HealthCheckResponse_NOT_SERVING)).Check(t.Context())
	if err == nil {
		t.Fatal("Check passed against a frontend that is not serving")
	}
}

func TestNewDoesNotConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	c := newClient(t, address)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := c.Check(ctx); err == nil || !strings.Contains(err.Error(), address) {
		t.Fatalf("Check of a stopped engine = %v, want an error naming %s", err, address)
	}
}
