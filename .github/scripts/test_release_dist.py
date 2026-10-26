#!/usr/bin/env python3
# Copyright (c) 2026, WSO2 LLC. (http://www.wso2.com).
#
# WSO2 LLC. licenses this file to you under the Apache License,
# Version 2.0 (the "License"); you may not use this file except
# in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied.  See the License for the
# specific language governing permissions and limitations
# under the License.

import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().with_name("release_dist.sh")


def git(cwd: Path, *args: str) -> str:
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


class ReleaseDistTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.remote = root / "remote.git"
        self.repo = root / "repo"
        git(root, "init", "-q", "--bare", str(self.remote))
        git(root, "init", "-q", "-b", "main", str(self.repo))
        git(self.repo, "config", "user.email", "release@example.com")
        git(self.repo, "config", "user.name", "Release Test")
        git(self.repo, "config", "commit.gpgsign", "false")
        git(self.repo, "config", "tag.gpgsign", "false")
        (self.repo / "README.md").write_text("test\n")
        git(self.repo, "add", "README.md")
        git(self.repo, "commit", "-q", "-m", "chore: start")
        git(self.repo, "remote", "add", "origin", str(self.remote))

    def tearDown(self):
        self.temp.cleanup()

    def release(self, *args: str) -> subprocess.CompletedProcess:
        return subprocess.run(["bash", str(SCRIPT), *args], cwd=self.repo, capture_output=True, text=True)

    def remote_tags(self) -> str:
        return git(self.repo, "ls-remote", "--tags", "origin")

    def test_pushes_the_tag(self):
        result = self.release("v0.1.0", "origin")
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("refs/tags/v0.1.0", self.remote_tags())

    def test_refuses_a_tag_the_remote_already_has(self):
        git(self.repo, "tag", "-a", "v0.1.0", "-m", "elsewhere")
        git(self.repo, "push", "-q", "origin", "refs/tags/v0.1.0")
        git(self.repo, "tag", "-d", "v0.1.0")
        result = self.release("v0.1.0", "origin")
        self.assertEqual(1, result.returncode)
        self.assertIn("already exists on origin", result.stderr)
        self.assertEqual("", git(self.repo, "tag", "--list", "v0.1.0"))

    def test_refuses_an_unreachable_remote_without_tagging(self):
        git(self.repo, "remote", "add", "missing", str(Path(self.temp.name) / "missing.git"))
        result = self.release("v0.1.0", "missing")
        self.assertEqual(1, result.returncode)
        self.assertIn("unable to check tag", result.stderr)
        self.assertEqual("", git(self.repo, "tag", "--list", "v0.1.0"))

    def test_removes_the_local_tag_when_the_push_fails(self):
        hook = self.remote / "hooks" / "pre-receive"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o755)
        result = self.release("v0.1.0", "origin")
        self.assertEqual(1, result.returncode)
        self.assertIn("removed the local tag v0.1.0", result.stderr)
        self.assertEqual("", git(self.repo, "tag", "--list", "v0.1.0"))

    def test_refuses_an_existing_local_tag(self):
        git(self.repo, "tag", "-a", "v0.1.0", "-m", "local")
        result = self.release("v0.1.0", "origin")
        self.assertEqual(1, result.returncode)
        self.assertIn("tag v0.1.0 already exists", result.stderr)

    def test_refuses_a_dirty_worktree(self):
        (self.repo / "README.md").write_text("changed\n")
        result = self.release("v0.1.0", "origin")
        self.assertEqual(1, result.returncode)
        self.assertIn("dirty worktree", result.stderr)

    def test_refuses_an_invalid_version(self):
        result = self.release("0.1", "origin")
        self.assertEqual(1, result.returncode)
        self.assertIn("usage:", result.stderr)


if __name__ == "__main__":
    unittest.main()
