# Development environment

This page shows how to set up a local machine to build, run and test Harbormaster. It is for contributors.

## Before you begin

Install:

- Go 1.27 (see `go.mod`)
- Node 26 (see `.nvmrc`)
- Docker, for the local Postgres test container and for image builds
- `make`, `curl`, `jq` and `python3` (the split smoke test uses them)

You do not need AWS credentials. Local runs use an in-memory fake ECS estate.

## Run it locally

1. Start Postgres and export the URL it prints:

    ```sh
    make test-db
    export HM_TEST_DATABASE_URL='postgres://...'
    ```

    If the container already exists, `make test-db` reuses it and prints a `docker port hm-test-pg` hint. Build the URL from that.

2. Start the server:

    ```sh
    make run
    ```

    This runs `serve --fake-aws` on `http://127.0.0.1:8480`. It migrates the database and seeds demo users, tags, permissions and a fake ECS estate.

3. Sign in as `sam` with password `harbormaster-e2e-1` (a global admin). `dana`, `omer` and `oncall` use the same password with narrower access.

!!! warning "Fake AWS data"
    Never use `--fake-aws` or `HM_FAKE_AWS` against a database that holds real users.

### Check it worked

Open `http://127.0.0.1:8480` and see the Overview page with demo clusters.

## Other local setups

| Goal | Command |
|---|---|
| Frontend work | `make run` in one terminal, `npm --prefix web run dev` in another. Vite proxies `/api` to port 8480 |
| Split roles | `make test-kv`, export `HM_TEST_KV_URLS`, then `make run-split` (web 8480, poller 8482, coordinator 8483) |
| MCP gateway | `HM_API_TOKENS=true make run`, create a token on My access, then `make run-mcp` (port 8481) |
| Real AWS code path against an emulator | [Local AWS with floci](local-aws-with-floci.md) |

`make e2e` also uses port 8481, so stop the gateway first.

## Build

```sh
make build
```

This builds the web app (when `web/package.json` exists) and writes `bin/harbormaster` and `bin/harbormaster-mcp`. The build overwrites the tracked placeholder `internal/web/dist/index.html`. Restore it before committing:

```sh
git checkout -- internal/web/dist/index.html
```

## Make targets

| Target | What it does |
|---|---|
| `make build` | Web app and both binaries |
| `make test` | Go tests, then web tests (Vitest) |
| `make lint` | gofmt, `go vet`, staticcheck, web lint and typecheck, third-party notices |
| `make test-db`, `make test-kv` | Start or reuse the Postgres, Valkey and Redis test containers |
| `make run`, `make run-mcp`, `make run-split` | Run locally |
| `make e2e`, `make soak`, `make split-smoke` | Longer suites. See [Testing](testing.md) |
| `make tf-check` | Validate every Terraform root. Never plans or applies |
| `make vulncheck` | `govulncheck` on every package; fails on called advisories not listed in `tools/vulncheck-accepted.txt` |

For all targets read the `Makefile`.

## Next steps

- [Repository layout](repository-layout.md)
- [Testing](testing.md)
- [Coding conventions](coding-conventions.md)
