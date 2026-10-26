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

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// releaseRepo is a repository with one commit and a bare origin, for release_dist.sh to act on.
type releaseRepo struct {
	t      *testing.T
	dir    string
	remote string
	env    []string
}

func newReleaseRepo(t *testing.T) *releaseRepo {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("release_dist.sh is a bash script")
	}
	root := t.TempDir()
	r := &releaseRepo{t: t, dir: filepath.Join(root, "repo"), remote: filepath.Join(root, "remote.git"), env: cleanEnv(t)}
	r.git(root, "init", "-q", "--bare", r.remote)
	r.git(root, "init", "-q", "-b", "main", r.dir)
	r.git(r.dir, "config", "user.email", "release@example.com")
	r.git(r.dir, "config", "user.name", "Release Test")
	r.git(r.dir, "config", "commit.gpgsign", "false")
	r.git(r.dir, "config", "tag.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(r.dir, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.git(r.dir, "add", "README.md")
	r.git(r.dir, "commit", "-q", "-m", "chore: start")
	r.git(r.dir, "remote", "add", "origin", r.remote)
	return r
}

// cleanEnv drops the variables that make git act on this repository (set inside hooks), so cwd decides.
func cleanEnv(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Fatal(err)
	}
	repoVars := map[string]bool{}
	for _, name := range strings.Fields(string(out)) {
		repoVars[name] = true
	}
	var env []string
	for _, entry := range os.Environ() {
		if name, _, _ := strings.Cut(entry, "="); !repoVars[name] {
			env = append(env, entry)
		}
	}
	return env
}

func (r *releaseRepo) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, r.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *releaseRepo) release(args ...string) (code int, stderr string) {
	r.t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "..", ".github", "scripts", "release_dist.sh"))
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir, cmd.Env = r.dir, r.env
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, errOut.String()
	case isExitError(err, &exit):
		return exit.ExitCode(), errOut.String()
	default:
		r.t.Fatal(err)
		return -1, ""
	}
}

func isExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError) //nolint:errorlint // exec.Command returns it unwrapped
	if ok {
		*target = e
	}
	return ok
}

func (r *releaseRepo) remoteTags() string { return r.git(r.dir, "ls-remote", "--tags", "origin") }

func (r *releaseRepo) localTag(version string) string { return r.git(r.dir, "tag", "--list", version) }

func TestReleasePushesTheTag(t *testing.T) {
	r := newReleaseRepo(t)
	if code, stderr := r.release("v0.1.0", "origin"); code != 0 {
		t.Fatalf("release = %d: %s", code, stderr)
	}
	if !strings.Contains(r.remoteTags(), "refs/tags/v0.1.0") {
		t.Fatal("the tag was not pushed")
	}
}

func TestReleaseAcceptsAReleaseBranch(t *testing.T) {
	r := newReleaseRepo(t)
	r.git(r.dir, "checkout", "-q", "-b", "release-0.1.x")
	if code, stderr := r.release("v0.1.1", "origin"); code != 0 {
		t.Fatalf("release = %d: %s", code, stderr)
	}
	if !strings.Contains(r.remoteTags(), "refs/tags/v0.1.1") {
		t.Fatal("the tag was not pushed")
	}
}

func TestReleaseRefusals(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(r *releaseRepo)
		version string
		want    string
	}{
		{"feature branch", func(r *releaseRepo) { r.git(r.dir, "checkout", "-q", "-b", "feature/x") }, "v0.1.0",
			"refusing to release from branch feature/x"},
		{"existing local tag", func(r *releaseRepo) { r.git(r.dir, "tag", "-a", "v0.1.0", "-m", "local") }, "v0.1.0",
			"tag v0.1.0 already exists"},
		{"tag on the remote", func(r *releaseRepo) {
			r.git(r.dir, "tag", "-a", "v0.1.0", "-m", "elsewhere")
			r.git(r.dir, "push", "-q", "origin", "refs/tags/v0.1.0")
			r.git(r.dir, "tag", "-d", "v0.1.0")
		}, "v0.1.0", "already exists on origin"},
		{"dirty worktree", func(r *releaseRepo) {
			if err := os.WriteFile(filepath.Join(r.dir, "README.md"), []byte("changed\n"), 0o600); err != nil {
				r.t.Fatal(err)
			}
		}, "v0.1.0", "dirty worktree"},
		{"invalid version", func(*releaseRepo) {}, "0.1", "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReleaseRepo(t)
			tt.prepare(r)
			code, stderr := r.release(tt.version, "origin")
			if code != 1 || !strings.Contains(stderr, tt.want) {
				t.Fatalf("release = %d, %q; want 1 with %q", code, stderr, tt.want)
			}
		})
	}
}

func TestReleaseRefusesAnUnreachableRemoteWithoutTagging(t *testing.T) {
	r := newReleaseRepo(t)
	r.git(r.dir, "remote", "add", "missing", filepath.Join(t.TempDir(), "missing.git"))
	code, stderr := r.release("v0.1.0", "missing")
	if code != 1 || !strings.Contains(stderr, "unable to check tag") {
		t.Fatalf("release = %d, %q", code, stderr)
	}
	if r.localTag("v0.1.0") != "" {
		t.Fatal("a local tag was left behind")
	}
}

func TestReleaseRemovesTheLocalTagWhenThePushFails(t *testing.T) {
	r := newReleaseRepo(t)
	hook := filepath.Join(r.remote, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // a hook must be executable
		t.Fatal(err)
	}
	code, stderr := r.release("v0.1.0", "origin")
	if code != 1 || !strings.Contains(stderr, "removed the local tag v0.1.0") {
		t.Fatalf("release = %d, %q", code, stderr)
	}
	if r.localTag("v0.1.0") != "" {
		t.Fatal("the local tag was not removed")
	}
}
