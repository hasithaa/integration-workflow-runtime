# api

The API server (Go, `workflow-api`): it serves the runtime's APIs and translates them into calls to the engine's
frontend through the Temporal Go SDK. It will serve the Workflow Management API, the namespace and token APIs, SSO
sign-in and roles, and the Admin Portal at `/admin` and the User Portal at `/portal` on the same port as its API.

Today it is a skeleton: it reads `conf/runtime.toml`, creates and migrates its own database, and serves
`GET /healthz` and `GET /readyz` over plain HTTP. See [docs/features/api-server.md](../docs/features/api-server.md).

```sh
make api                                                   # build/workflow-api
./build/workflow-api start --runtime-config conf/runtime.toml
```

| Package | What it does |
|---|---|
| `cmd/workflow-api` | Thin `main` |
| `internal/cli` | The `version` and `start` commands, graceful shutdown on SIGINT and SIGTERM |
| `internal/config` | `[api]`, `[database]` and the frontend's address from `[engine]` in `runtime.toml` |
| `internal/database` | Creates the database when missing and applies the embedded migrations in `migrations/<type>/` |
| `internal/engine` | The engine client (Temporal SDK), connected lazily |
| `internal/server` | The HTTP server and the health endpoints |

A new migration is `internal/database/migrations/<type>/NNNN_description.sql` for each of `postgresql`, `mysql`
and `sqlite`, numbered after the last one; statements end with `;` at the end of a line. Never change a migration
that has shipped.
