# Repository layout

This page maps the repository: the commands, the Go packages, the web app, Terraform and tooling. It is for contributors finding where code lives.

## Top level

| Path | Contents |
|---|---|
| `cmd/` | The three binaries |
| `internal/` | All Go packages (not importable from outside the module) |
| `web/` | React single-page app, Vitest tests and Playwright e2e specs |
| `terraform/` | Modules (`harbormaster`, `harbormaster-access`, `harbormaster-mcp`) and examples |
| `tools/` | Scripts for checks, smoke tests, image checks and the floci harness |
| `docs/` | Design notes and ops runbooks |
| `harbormaster-docs/` | This documentation site |
| `third_party/licenses/` | Licence copies for bundled components |
| `Dockerfile`, `Dockerfile.mcp`, `Dockerfile.scanner` | Server, MCP gateway and scanner images |

## Commands (`cmd/`)

| Package | Purpose |
|---|---|
| `cmd/harbormaster` | The server and embedded web UI. Commands include `serve`, `healthcheck`, `version` and `create-admin` |
| `cmd/harbormaster-mcp` | The public, read-only MCP gateway. It holds no credentials and uses the caller's own token |
| `cmd/harbormaster-scanner` | The scan task. Reads a job from the scan bucket, scans ECR images with Syft and Grype, writes results |

## Packages (`internal/`)

| Package | Purpose |
|---|---|
| `api` | HTTP layer: middleware, error mapping, handlers and WebSockets |
| `apitoken` | Format of bearer tokens (`hmp_<id>_<secret>`); shape checks only |
| `auth` | Password hashing, sessions, CSRF, login throttling, first-run setup, SSO sign-in glue |
| `authz` | Tag-based permission model |
| `awsx` | AWS SDK behind narrow interfaces |
| `config` | Loads settings from `HM_*` environment variables |
| `configsearch` | Parses and matches config search queries over task definition names |
| `coordinator` | Singleton jobs: notifications, off-hours, scans, platform resolver, rollups, retention |
| `cost` | Cost and pricing calculations |
| `deploy` | Converts described task definitions to register-ready form and back |
| `devseed` | Demo users, tags and history for `serve --fake-aws` |
| `execbridge` | Browser terminal to ECS Exec bridge, speaking the SSM data-channel protocol |
| `focus` | Tracks which pages are open so owning pollers read them often |
| `hmtools` | The read-only MCP tool table and the one upstream client that calls Harbormaster for it, shared with the agent service |
| `kv` | The one door to a Valkey or Redis server, limited to commands both engines share |
| `lease` | Ownership of polling slices and the coordinator role |
| `limits` | Cross-task limits: sessions per user, rate limits |
| `logs` | CloudWatch Logs reading, stream naming, filters and paging |
| `mcpgw` | The MCP gateway server (Streamable HTTP) |
| `metrics` | CloudWatch metrics via `GetMetricData`, with a shared cache |
| `model` | Shared data types |
| `network` | Logic behind the Networks pages |
| `notify` | Alert routing, quiet hours, message rendering, sending |
| `oauthsrv` | OAuth 2.1 authorization server for MCP connectors |
| `offhours` | Weekly awake grid, scheduled actions, wake and grace rules |
| `platforms` | CPU architectures supported by x86 Fargate service images |
| `poller` | Keeps the database snapshot of the ECS estate current, one lease per account and region slice |
| `registry` | Image manifests from public registries, through the safe dialer |
| `rollup` | Daily analytics tables built before retention deletes raw rows |
| `safedial` | Outbound address guard against DNS rebinding and internal targets |
| `saml` | SAML 2.0 service-provider logic: keys, metadata, request, verification |
| `scan` | Pure image-security logic: reference parsing and validation of scanner output |
| `scanjob` | Scan loop on the poller leader |
| `scanner` | The scan task's implementation |
| `secretbox` | AES-256-GCM sealing of small secrets under a key derived from `HM_SESSION_KEY` |
| `signal` | Small wake-up messages between tasks |
| `snapcache` | Unfiltered read models of polled rows in Valkey or Redis |
| `status` | Service and cluster health from polled ECS data |
| `storage` | Maps task definitions to EFS file systems |
| `store` | Postgres access: pool, migrations, queries |
| `telemetry` | Metrics registry with a hand-written Prometheus text writer |
| `timeline` | Events shown on a service's Timeline tab |
| `web` | Embeds the built SPA with fallback routing |

## Related pages

- [Architecture](../getting-started/architecture.md)
- [Frontend](frontend.md)
