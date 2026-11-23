# Local AWS with floci

This page explains the floci harness in `tools/floci`: a local AWS emulator plus Postgres that lets Harbormaster's real AWS code path run against a seeded ECS estate. It is for contributors who need more than the in-memory fake.

floci is an open-source AWS emulator (image `floci/floci:2.4.0`). ECS runs in mock mode, and no Docker socket is mounted, so no task containers start. Both ports bind to loopback.

## Safety

Every script sources `tools/floci/env.sh`. It refuses (exit 2) if `AWS_ENDPOINT_URL` is not a local host, or if `AWS_PROFILE` is anything but `floci`. It then points the AWS CLI and SDKs at an isolated config and credentials file in that directory, so `~/.aws` is never read.

## Before you begin

- Docker with Compose.
- `make build` output in `bin/` (the check targets build it for you).
- For the Playwright checks: `cd web && npx playwright install chromium`.

## Typical run

```sh
make floci-up floci-seed floci-check floci-down
```

## Make targets

| Target | What it does |
|---|---|
| `make floci-selfcheck` | Tests `env.sh` refusal rules |
| `make floci-up` | Starts floci (port 4566) and Postgres (port 55433) and waits until healthy |
| `make floci-seed` | Seeds a demo ECS estate. Idempotent |
| `make floci-serve` | Prepares the database and runs the server in the foreground (port 8490) |
| `make floci-check` | Builds, prepares the database and runs the Playwright floci suite |
| `make floci-mcp-check` | Checks access tokens, OAuth discovery and the MCP gateway (port 8491, gateway 8495) |
| `make floci-sanity` | Drives write and core-feature paths through the API of a running server and verifies each in floci with the AWS CLI |
| `make floci-down` | Removes the containers and volumes, and the generated admin password file |

`floci-check` and `floci-mcp-check` refuse to run if `internal/web/dist/index.html` has local edits, because the build overwrites it. They restore it afterwards. Do not run `floci-serve`, `floci-check` and `floci-mcp-check` at the same time: they share one Postgres.

## Scripts

| Script | Purpose |
|---|---|
| `env.sh` | Pins the shell to floci. Source it |
| `prepare.sh` | Applies migrations and creates the harness admin. The password is written to `tools/floci/.admin-password` (git-ignored, mode 600) and never printed |
| `seed.sh` | Seeds the ECS estate with `--profile floci` and explicit regions |
| `serve.sh`, `serve-tokens.sh` | Run the server (tokens and OAuth on for the second) |
| `connect.sh` | Signs in as admin, connects the `floci` account, tags a cluster Prod, creates user `omer`, makes sample changes. Idempotent |
| `sanity.sh` | Functional sanity check. Prints `ok`, `FAIL` or `skip` per check and exits 1 on failure |
| `mcp-e2e.sh` | Token, gateway, read-only gate, visibility and OAuth checks |

Each script prints help with `--help`.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `HM_FLOCI_PORT` | `8490` (`8491` for the tokens server) | Server port |
| `HM_FLOCI_MCP_PORT` | `8495` | Gateway port for `mcp-e2e.sh` |
| `HM_E2E_SCREENSHOT_DIR` | unset | Keep a screenshot per route, width and theme in `floci-check` |

## Next steps

- [Testing](testing.md)
- [Dev environment](dev-environment.md)
