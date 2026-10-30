# API server

Status: in progress · since 0.1.0 (in development)

## What it does

`workflow-api` is the runtime's API server. It will serve the Workflow Management API, the namespace and token
APIs, SSO sign-in and the portals, translating those calls into calls to the engine's frontend through the
Temporal Go SDK. Today it is the skeleton those build on: it reads the same `conf/runtime.toml` as the engine,
creates and migrates its own database, connects to the engine, and reports its health.

```sh
workflow-api start --runtime-config conf/runtime.toml
workflow-api version
```

## Configuration

### `[api]`

| Key | Default | Meaning |
|---|---|---|
| `bindAddress` | `127.0.0.1` | Address the HTTP server listens on |
| `port` | 9090 | HTTP port |
| `engineAddress` | this node's frontend | `host:port` of the engine's frontend: `[engine] broadcastAddress`, else `[engine] bindAddress` unless it is `0.0.0.0`, else `127.0.0.1`; with `[engine.ports] frontend` (7233) |

Unknown keys in `[api]` are rejected. From `[engine]` the API server reads only `bindAddress`, `broadcastAddress`
and `ports.frontend`; the engine validates the rest of that table.

### `[database]`

The API server uses the engine's connection (`type`, `host`, `port`, `user`, `password`, `maxConns`,
`[database.tls]`; see [runtime configuration](runtime-config.md)) with a database of its own, as Temporal does:
one connection, a default name for each database, every name overridable.

| Key | Default | Meaning |
|---|---|---|
| `apiDatabaseName` | `workflow_api` | The API server's database on PostgreSQL and MySQL; must differ from `name` and `visibilityName`. Letters, digits and underscores, at most 63, not starting with a digit |
| `path` (SQLite) | | The API server's database is the file `<apiDatabaseName>.db` in the same directory |
| `inMemory` (SQLite) | false | The API server's database is in memory too |

`name` and `visibilityName` are Temporal's and are only checked against `apiDatabaseName`. Unknown keys in
`[database]` are rejected, as the engine does.

### HTTP endpoints

| Endpoint | Answers |
|---|---|
| `GET /healthz` | 200 `{"status":"ok"}` while the process runs |
| `GET /readyz` | 200 when every check passes, 503 otherwise, with the result per check: `{"status":"not ready","checks":{"database":"ok","engine":"failed"}}` |

## Behavior

- On start the API server creates its database when it is missing (PostgreSQL through the `postgres` database,
  MySQL through the server), then applies the migrations it does not have yet. The user needs the right to create
  a database, or the database must exist already.
- Migrations are numbered SQL files embedded in the binary, one set per database type. Applied versions are
  recorded in the `schema_version` table; a second start applies nothing. On PostgreSQL and SQLite each
  migration and its record commit in one transaction; MySQL commits DDL statement by statement.
- Nodes starting together take a database lock first (PostgreSQL `pg_advisory_lock`, MySQL `GET_LOCK`, waiting
  up to 2 minutes), so each migration runs once. SQLite has one process.
- A database at a newer schema version than the binary knows is refused, so an older API server does not run
  against a newer schema.
- The API server starts whether or not the engine is up; its client connects on first use and reconnects. While
  the engine is down, `/readyz` reports `engine` failed.
- Each readiness check (a database ping, the frontend's gRPC health) has 3 seconds. A failure is logged with its
  cause; the response names only the check.
- SIGINT or SIGTERM stops accepting connections and lets requests in flight finish, for up to 15 seconds.
- The start fails, exit code 1, when the configuration is invalid, the database cannot be created or migrated,
  or the port is taken.
- Logs are JSON on standard error.

## Limits

- Plain HTTP only; TLS on the API server's port is a later task. Keep `bindAddress` on loopback or a private
  network until then.
- The API server's database uses the same server, user and password as Temporal's.
- The engine client uses the frontend without TLS or a token, like the engine's `--allow-no-auth` mode.

## Not covered yet

The Workflow Management API, namespace and token APIs, SSO sign-in and roles, the portals, TLS, product tables in
the database, and the launcher's `api` profile.

## Tests

| Test | Covers |
|---|---|
| `api/internal/config/config_test.go` | Defaults, the engine address, keys left to the engine, SQLite files, environment passwords, every validation rule, the distribution's `runtime.toml` |
| `api/internal/database/database_test.go` | Create and migrate once on SQLite (file, memory), PostgreSQL and MySQL; concurrent starts; the migration lock; rollback of a failed migration; refusing a newer schema; TLS settings. PostgreSQL and MySQL run when `WORKFLOW_TEST_DB_PASSWORD` is set (see [TESTING](../guides/TESTING.md)) |
| `api/internal/engine/engine_test.go` | Health against a serving and a not-serving frontend, no connection at creation |
| `api/internal/server/server_test.go` | `/healthz`, `/readyz` per check, the check timeout, graceful shutdown |
| `api/internal/cli/cli_test.go` | Commands and flags, a full start that turns ready, a start with the engine down, database and listen failures |
