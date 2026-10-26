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

package balsuite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// servingTemporal answers every operator call successfully and reports SERVING.
const servingTemporal = "#!/bin/sh\ncase \"$2 $3\" in 'cluster health') echo SERVING;; esac\nexit 0\n"

// idleEngine runs until terminated.
const idleEngine = "#!/bin/sh\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n"

func script(t *testing.T, dir, name, content string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are shell scripts")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil { //nolint:gosec // an executable test fake
		t.Fatal(err)
	}
	return path
}

func options(t *testing.T, temporal, engine, bal string) Options {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests", "ballerina", "smoke"), 0o750); err != nil {
		t.Fatal(err)
	}
	return Options{
		Suite:          "smoke",
		Root:           root,
		Engine:         script(t, root, "engine", engine),
		EngineArgs:     []string{"--allow-no-auth"},
		Bal:            script(t, root, "bal", bal),
		Temporal:       script(t, root, "temporal", temporal),
		LogDir:         filepath.Join(root, "logs"),
		StartupTimeout: 10 * time.Second,
		TestTimeout:    10 * time.Second,
	}
}

func TestRunPassesWhenTheSuitePasses(t *testing.T) {
	o := options(t, servingTemporal, idleEngine, "#!/bin/sh\necho \"$@\" > \"$PWD/ran\"\nexit 0\n")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), o, &stdout, &stderr); code != 0 {
		t.Fatalf("Run = %d, want 0; stderr: %s", code, stderr.String())
	}
	ran, err := os.ReadFile(filepath.Join(o.Root, "tests", "ballerina", "smoke", "ran"))
	if err != nil || strings.TrimSpace(string(ran)) != "test" {
		t.Fatalf("bal was run with %q (%v), want `test` in the suite directory", ran, err)
	}
	if !strings.Contains(stdout.String(), "SERVING on "+Address+"; running the smoke suite") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(o.LogDir, "engine-smoke.log")); err != nil {
		t.Fatalf("engine log: %v", err)
	}
}

func TestRunReturnsTheSuiteExitCodeAndPrintsTheLogTail(t *testing.T) {
	engine := "#!/bin/sh\necho engine says hello\n" + idleEngine
	o := options(t, servingTemporal, engine, "#!/bin/sh\nexit 3\n")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), o, &stdout, &stderr); code != 3 {
		t.Fatalf("Run = %d, want 3", code)
	}
	if !strings.Contains(stderr.String(), "engine says hello") {
		t.Fatalf("stderr = %q, want the engine log tail", stderr.String())
	}
}

func TestRunRunsPrepareBeforeTheEngine(t *testing.T) {
	o := options(t, servingTemporal, idleEngine, "#!/bin/sh\nexit 0\n")
	o.Prepare = func(context.Context) error { return errors.New("schema setup failed") }
	var stderr bytes.Buffer
	if code := Run(context.Background(), o, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("Run = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "schema setup failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFailsWhenTheEngineExitsEarly(t *testing.T) {
	o := options(t, "#!/bin/sh\nexit 1\n", "#!/bin/sh\nexit 7\n", "#!/bin/sh\nexit 0\n")
	var stderr bytes.Buffer
	if code := Run(context.Background(), o, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("Run = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "exited with code 7 before it was ready") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFailsWhenTheEngineNeverServes(t *testing.T) {
	o := options(t, "#!/bin/sh\nexit 1\n", idleEngine, "#!/bin/sh\nexit 0\n")
	o.StartupTimeout = 2 * time.Second
	var stderr bytes.Buffer
	if code := Run(context.Background(), o, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("Run = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "was not SERVING") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunFailsWhenTheEngineCannotStart(t *testing.T) {
	o := options(t, servingTemporal, idleEngine, "#!/bin/sh\nexit 0\n")
	o.Engine = filepath.Join(o.Root, "absent")
	var stderr bytes.Buffer
	if code := Run(context.Background(), o, &bytes.Buffer{}, &stderr); code != 1 {
		t.Fatalf("Run = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unable to start") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestEnsureNamespaceAcceptsExistingNamespaceAndAttribute(t *testing.T) {
	temporal := script(t, t.TempDir(), "temporal", "#!/bin/sh\n"+
		"case \"$2 $3\" in 'namespace create'|'search-attribute create') echo 'already exists' >&2; exit 1;; esac\nexit 0\n")
	if err := ensureNamespace(context.Background(), temporal, 5*time.Second); err != nil {
		t.Fatalf("ensureNamespace = %v, want nil", err)
	}
}

func TestEnsureNamespaceReportsFailures(t *testing.T) {
	tests := []struct {
		name, failing, want string
	}{
		{"create", "namespace create", "could not create namespace"},
		{"attribute", "search-attribute create", "could not add search attribute WorkflowKind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			temporal := script(t, t.TempDir(), "temporal", "#!/bin/sh\n"+
				"case \"$2 $3\" in '"+tt.failing+"') echo 'permission denied' >&2; exit 1;; esac\nexit 0\n")
			err := ensureNamespace(context.Background(), temporal, 5*time.Second)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("ensureNamespace = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEnsureNamespaceTimesOutWhenDescribeKeepsFailing(t *testing.T) {
	temporal := script(t, t.TempDir(), "temporal", "#!/bin/sh\ncase \"$2 $3\" in 'namespace describe') exit 1;; esac\nexit 0\n")
	err := ensureNamespace(context.Background(), temporal, 0)
	if err == nil || !strings.Contains(err.Error(), "did not become available") {
		t.Fatalf("ensureNamespace = %v, want the availability timeout", err)
	}
}

func TestRunQuietTurnsATimeoutIntoAFailedCall(t *testing.T) {
	sleep := script(t, t.TempDir(), "slow", "#!/bin/sh\nsleep 30\n")
	r := runQuiet(context.Background(), 200*time.Millisecond, sleep)
	if r.code != exitCodeTimeout || !strings.Contains(r.stderr, "timed out after 0s") {
		t.Fatalf("runQuiet = %+v, want the timeout result", r)
	}
}

func TestRunQuietReportsAMissingCommand(t *testing.T) {
	r := runQuiet(context.Background(), time.Second, filepath.Join(t.TempDir(), "absent"))
	if r.code != 127 || r.stderr == "" {
		t.Fatalf("runQuiet = %+v, want code 127 with a message", r)
	}
}

func TestRunBalTestKillsTheProcessGroupOnTimeout(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child.pid")
	bal := script(t, dir, "bal", "#!/bin/sh\nsleep 30 &\necho $! > "+marker+"\nwait\n")
	var stderr bytes.Buffer
	if code := runBalTest(bal, dir, 3*time.Second, &stderr); code != exitCodeTimeout {
		t.Fatalf("runBalTest = %d, want %d", code, exitCodeTimeout)
	}
	if !strings.Contains(stderr.String(), "did not finish within 3s") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	pid, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(pid)))
	if err != nil {
		t.Fatal(err)
	}
	for range 50 { // the killed child is reaped by init, not instantly
		if err := syscall.Kill(child, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("child %d of the timed-out bal test is still running", child)
}

func TestRunBalTestReportsAnUnstartableCommand(t *testing.T) {
	var stderr bytes.Buffer
	if code := runBalTest(filepath.Join(t.TempDir(), "absent"), t.TempDir(), time.Second, &stderr); code != 1 {
		t.Fatalf("runBalTest = %d, want 1", code)
	}
}

func TestEngineStopKillsAnEngineThatIgnoresTerminate(t *testing.T) {
	engine := script(t, t.TempDir(), "engine", "#!/bin/sh\ntrap '' TERM\nwhile :; do sleep 1; done\n")
	previous := stopTimeout
	stopTimeout = time.Second
	t.Cleanup(func() { stopTimeout = previous })
	p, err := startEngine(engine, nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { p.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(stopTimeout + 5*time.Second):
		t.Fatal("stop did not return after the kill fallback")
	}
	if done, _ := p.exitedWith(); !done {
		t.Fatal("the engine is still running")
	}
}

func TestPrintLogTailPrintsTheLastLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printLogTail(&out, path, 2)
	if out.String() != "--- last 2 lines of "+path+" ---\ntwo\nthree\n" {
		t.Fatalf("output = %q", out.String())
	}
	printLogTail(&out, filepath.Join(t.TempDir(), "absent.log"), 2)
}
