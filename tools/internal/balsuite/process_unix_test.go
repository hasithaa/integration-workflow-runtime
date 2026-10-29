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

//go:build !windows

package balsuite

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
