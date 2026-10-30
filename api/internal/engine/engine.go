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

// Package engine is the API server's client of the workflow engine's frontend.
package engine

import (
	"context"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

// Client reaches the engine's frontend through the Temporal SDK. It connects on first use and reconnects after
// failures, so the API server starts while the engine is down.
type Client struct {
	address  string
	temporal client.Client
}

// New returns a client for the frontend at address without connecting to it.
func New(address string, logger *slog.Logger) (*Client, error) {
	c, err := client.NewLazyClient(client.Options{HostPort: address, Logger: log.NewStructuredLogger(logger)})
	if err != nil {
		return nil, fmt.Errorf("unable to create the engine client for %s: %w", address, err)
	}
	return &Client{address: address, temporal: c}, nil
}

// Check reports whether the engine's frontend answers its health check.
func (c *Client) Check(ctx context.Context) error {
	if _, err := c.temporal.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		return fmt.Errorf("engine at %s: %w", c.address, err)
	}
	return nil
}

// Close closes the connection to the engine.
func (c *Client) Close() {
	c.temporal.Close()
}
