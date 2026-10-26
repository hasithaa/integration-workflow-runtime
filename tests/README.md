# tests

| Path | What |
|---|---|
| `ballerina/smoke` | Smoke suite: the basics every engine build must keep (every pull request) |
| `ballerina/full` | Full suite: workflow features against the runtime (on `main`, nightly, on demand) |
| `engine/temporal-test.yaml` | Engine configuration for the suites: in-memory SQLite, ports 17xxx |

The runner that starts the engine, prepares the `default` namespace, runs a suite and stops the engine is
`tools/cmd/balsuite` (`make test-smoke`, `make test-full`). See [docs/guides/TESTING.md](../docs/guides/TESTING.md). Contract tests (the Manager against an integration's own
Workflow Management API) and upgrade tests join here as those components arrive.
