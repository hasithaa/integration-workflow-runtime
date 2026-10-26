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

import os
import subprocess
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

import run_suite


def completed(returncode: int = 0, stdout: str = "", stderr: str = "") -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess([], returncode, stdout, stderr)


class RunQuietTest(unittest.TestCase):
    def test_returns_the_result(self):
        with mock.patch.object(subprocess, "run", return_value=completed(0, "ok")) as run:
            self.assertEqual("ok", run_suite.run_quiet(["temporal"]).stdout)
        self.assertEqual(run_suite.CLI_TIMEOUT_SECONDS, run.call_args.kwargs["timeout"])

    def test_turns_a_timeout_into_a_failed_call(self):
        with mock.patch.object(subprocess, "run", side_effect=subprocess.TimeoutExpired(["temporal"], 5)):
            result = run_suite.run_quiet(["temporal"], timeout=5)
        self.assertEqual(124, result.returncode)
        self.assertIn("timed out after 5s", result.stderr)


class RunBalTestTest(unittest.TestCase):
    def test_returns_the_exit_code(self):
        with tempfile.TemporaryDirectory() as suite:
            script = Path(suite) / "bal"
            script.write_text("#!/bin/sh\nexit 3\n")
            script.chmod(0o755)
            self.assertEqual(3, run_suite.run_bal_test(str(script), Path(suite), timeout=5))

    @unittest.skipIf(os.name == "nt", "process groups")
    def test_kills_the_process_group_on_timeout(self):
        with tempfile.TemporaryDirectory() as suite:
            marker = Path(suite) / "child.pid"
            script = Path(suite) / "bal"
            script.write_text(f"#!/bin/sh\nsleep 30 &\necho $! > {marker}\nwait\n")
            script.chmod(0o755)
            self.assertEqual(124, run_suite.run_bal_test(str(script), Path(suite), timeout=3))
            child = int(marker.read_text())
            for _ in range(50):  # the killed child is reaped by init, not instantly
                try:
                    os.kill(child, 0)
                except ProcessLookupError:
                    return
                time.sleep(0.1)
            self.fail(f"child {child} of the timed-out bal test is still running")


class StopTest(unittest.TestCase):
    def test_terminates_a_running_engine(self):
        engine = mock.Mock(**{"poll.return_value": None})
        run_suite.stop(engine)
        engine.terminate.assert_called_once()
        engine.wait.assert_called_once_with(timeout=30)
        engine.kill.assert_not_called()

    def test_kills_an_engine_that_ignores_terminate(self):
        engine = mock.Mock(**{"poll.return_value": None})
        engine.wait.side_effect = [subprocess.TimeoutExpired(["engine"], 30), 0]
        run_suite.stop(engine)
        engine.kill.assert_called_once()

    def test_leaves_an_exited_engine_alone(self):
        engine = mock.Mock(**{"poll.return_value": 1})
        run_suite.stop(engine)
        engine.terminate.assert_not_called()


class WaitUntilServingTest(unittest.TestCase):
    def test_returns_once_serving(self):
        engine = mock.Mock(**{"poll.return_value": None})
        with mock.patch.object(run_suite, "run_quiet", return_value=completed(0, "SERVING")):
            run_suite.wait_until_serving("temporal", engine, timeout=5)

    def test_fails_when_the_engine_exits(self):
        engine = mock.Mock(returncode=1, **{"poll.return_value": 1})
        with self.assertRaisesRegex(RuntimeError, "exited with code 1"):
            run_suite.wait_until_serving("temporal", engine, timeout=5)

    def test_fails_after_the_timeout(self):
        engine = mock.Mock(**{"poll.return_value": None})
        with mock.patch.object(run_suite, "run_quiet", return_value=completed(1)):
            with self.assertRaisesRegex(RuntimeError, "not SERVING"):
                run_suite.wait_until_serving("temporal", engine, timeout=0)


class EnsureNamespaceTest(unittest.TestCase):
    def test_accepts_an_existing_namespace_and_attribute(self):
        exists = completed(1, stderr="Namespace already exists")
        with mock.patch.object(run_suite, "run_quiet", side_effect=[exists, completed(0), exists]) as calls:
            run_suite.ensure_namespace("temporal", timeout=5)
        self.assertIn("WorkflowKind", calls.call_args.args[0])

    def test_fails_when_the_namespace_cannot_be_created(self):
        with mock.patch.object(run_suite, "run_quiet", return_value=completed(1, stderr="connection refused")):
            with self.assertRaisesRegex(RuntimeError, "could not create namespace"):
                run_suite.ensure_namespace("temporal", timeout=5)

    def test_fails_when_the_search_attribute_cannot_be_added(self):
        denied = completed(1, stderr="permission denied")
        with mock.patch.object(run_suite, "run_quiet", side_effect=[completed(0), completed(0), denied]):
            with self.assertRaisesRegex(RuntimeError, "could not add search attribute"):
                run_suite.ensure_namespace("temporal", timeout=5)


if __name__ == "__main__":
    unittest.main()
