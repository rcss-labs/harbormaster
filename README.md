# Harbormaster

Harbormaster is one Go binary that serves an embedded React single-page app for viewing AWS ECS. It polls ECS across AWS accounts and regions into Postgres, and uses tag-based permissions to decide what each user can see.

## Prerequisites

- Go 1.26
- Node 22
- Docker (for the local Postgres test container)

## Run it locally

```sh
make test-db                        # start (or reuse) the hm-test-pg container and print its URL
export HM_TEST_DATABASE_URL='postgres://...'   # the URL make test-db prints
make run                            # http://127.0.0.1:8480, in-memory fake AWS estate
```

`make run` starts `serve --fake-aws`: it migrates the database, seeds demo users, tags and permissions, and serves a fake ECS estate, so no AWS credentials are needed. Sign in as `pavel` / `harbormaster-e2e-1` (a global admin). `dana`, `omer` and `oncall` use the same password with narrower access.

For frontend work, run `make run` in one terminal and `npm --prefix web run dev` in another. Vite serves the SPA and proxies `/api` to port 8480.

## Make targets

- `make build`: build the web UI (when `web/package.json` exists) and the `bin/harbormaster` binary
- `make test`: run Go tests, plus web tests (Vitest) when the web app exists. Go tests need `HM_TEST_DATABASE_URL`
- `make lint`: check gofmt, run `go vet`, and run web lint and typecheck
- `make test-db`: start (or reuse) the local Postgres test container `hm-test-pg`
- `make run`: run the server locally against fake AWS data and the test database
- `make e2e`: build, then run the Playwright smoke tests against the real server (`serve --fake-aws` on `127.0.0.1:8481`). Requires `HM_TEST_DATABASE_URL`. Each run recreates a dedicated `hm_e2e` schema in that database, so it never touches your other data. Install the browser once with `npx --prefix web playwright install chromium`. Port 8481 must be free.

## Configuration

All settings are environment variables. Durations use Go syntax (`5s`, `12h`).

| Variable | Default | Meaning |
| --- | --- | --- |
| `HM_LISTEN` | `:8480` | Address the HTTP server listens on |
| `HM_DATABASE_URL` | | Postgres URL. Either this or `HM_DATABASE_SECRET_ARN` is required |
| `HM_DATABASE_SECRET_ARN` | | Secrets Manager secret holding the database credentials (a postgres URL or RDS secret JSON) |
| `HM_SESSION_KEY` | | Secret key mixed into session id hashes. Set a long random value in production |
| `HM_BASE_URL` | `http://127.0.0.1:8480` | Public URL of the app |
| `HM_COOKIE_SECURE` | `true` | Set the Secure flag on cookies. Use `false` only for plain-HTTP local runs |
| `HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN` | | Secrets Manager secret with the initial `admin` password (see First run) |
| `HM_FAKE_AWS` | `false` | Local demo only, same as `serve --fake-aws`. Seeds demo users (admin `pavel` with a published password) and refuses to run against a database that already has users or accounts. Never set in production |
| `HM_TRUSTED_PROXY` | `false` | Trust the rightmost `X-Forwarded-For` entry as the client IP. Enable only behind a proxy that appends the real client address (the Terraform module sets it for the ALB) |
| `HM_LOG_LEVEL` | `info` | Log level |
| `HM_SESSION_IDLE` | `12h` | Idle session timeout |
| `HM_SESSION_ABSOLUTE` | `168h` | Maximum session lifetime |
| `HM_LOGIN_MAX_PER_USER` | `5` | Login attempts allowed per user in the window |
| `HM_LOGIN_MAX_PER_IP` | `30` | Login attempts allowed per client IP in the window |
| `HM_LOGIN_WINDOW` | `15m` | Login rate-limit window |
| `HM_POLL_SERVICES` | `5s` | Service poll interval |
| `HM_POLL_RUNNING_TASKS` | `5s` | Running-task poll interval |
| `HM_POLL_FULL_TASKS` | `30s` | Full task poll interval |
| `HM_POLL_STOPPED_TASKS` | `15s` | Stopped-task poll interval |
| `HM_POLL_CLUSTERS` | `60s` | Cluster poll interval |
| `HM_POLL_SERVICE_LIST` | `60s` | Service list poll interval |
| `HM_POLL_COUNTS_HISTORY` | `5m` | Task-count history sampling interval |
| `HM_POLL_MAX_INFLIGHT` | `8` | Maximum concurrent AWS calls |
| `HM_POLL_JITTER` | `500ms` | Random jitter added to poll schedules |
| `HM_RETENTION_STOPPED_TASKS` | `168h` | How long stopped tasks are kept |
| `HM_RETENTION_SERVICE_EVENTS` | `720h` | Service events |
| `HM_RETENTION_COUNTS_HISTORY` | `168h` | Task-count history |
| `HM_RETENTION_OPERATION_LOG` | `2160h` | Operation log |
| `HM_RETENTION_DEPLOYMENT_HISTORY` | `2160h` | Deployment history |
| `HM_RETENTION_AUDIT_LOG` | `8760h` | Audit log |
| `HM_RETENTION_ORPHAN_TAGS` | `720h` | Tags whose resource no longer exists |
| `HM_PAGE_SIZE_DEFAULT` | `100` | Default API page size |
| `HM_PAGE_SIZE_MAX` | `500` | Maximum API page size |

## First run

On a database with no users, the server needs a first global admin:

- **Setup token.** The server logs `no users exist; open the app and create the first global admin with setup token <token>`. Open the app, enter that token and choose a username and password. The token is one-time and is regenerated on each start until a user exists.
- **Bootstrap secret.** Alternatively set `HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN` to a Secrets Manager secret holding the password; on first start (real AWS mode) the server creates an `admin` user from it. The secret value is never logged.
- **CLI.** `harbormaster create-admin` creates an administrator account directly.

`--fake-aws` skips all of this by seeding demo users.
