# launcher

Planned. One process per node that:

- reads `conf/runtime.toml` and the profiles to run;
- writes Temporal's configuration (persistence, TLS, JWT authorizer trusting the API server's keys) and starts
  `workflow-engine` with the services of those profiles, including the internal frontend when auth is on;
- starts the API server (`workflow-api`) for the `api` profile;
- runs `workflow-engine-sql` on first start and on upgrade, and refuses an engine more than one Temporal minor
  ahead of the version recorded in the API server's database;
- restarts a component on failure and reports health.

Likely a second command in the `engine` module, so the distribution ships one Go binary for both.
