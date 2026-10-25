# Engine

Status: available · since 0.1.0 (in development)

## What it does

`workflow-engine` runs Temporal's services (frontend, history, matching, worker, internal frontend) in one binary
built from the pinned Temporal server module. Integrations' `ballerina/workflow` workers connect to its frontend.

## Configuration

| Command or flag | Default | Meaning |
|---|---|---|
| `workflow-engine version` | | Prints the product version and the embedded Temporal server version |
| `workflow-engine start --config-file <file>` | required | Temporal server configuration (YAML); the launcher will write it from `conf/runtime.toml` |
| `--service <name>` (repeatable) | all services | `frontend`, `internal-frontend`, `history`, `matching`, `worker` |
| `--allow-no-auth` | off | Start without an authorizer; development and tests only |

## Behavior

- Refuses to start when the configuration sets no authorizer and `--allow-no-auth` is not given.
- Refuses to start when an authorizer is set without a claim mapper: Temporal's no-op claim mapper gives every
  caller system-admin claims, so the authorizer would admit anyone.
- Exits with code 1 and a message when the configuration cannot be loaded or the server cannot start; code 2 for
  an unknown command.
- Persistence plugins built in: PostgreSQL, MySQL, SQLite.

## Limits

- One Temporal server version per build (v1.32.1 today); upgrades move one minor version at a time.
- The history shard count is fixed when the database is first set up and cannot change later.
- SQL persistence only; SQLite is for development and tests.

## Not covered yet

The engine endpoint, our authorizer, TLS and token configuration from `runtime.toml`, and the launcher's profiles.

## Tests

| Test | Covers |
|---|---|
| `engine/internal/cli/cli_test.go` | Usage, version, flags, configuration errors, the authorizer and claim mapper refusals, the start path |
| `tests/ballerina/smoke` | A real engine runs workflows, activities, data events and failures |
| `tests/ballerina/full` | Retries, timers, child workflows, human tasks, execution history |
