# Floci review harness

Runs Harbormaster's real AWS code path (no `--fake-aws`) against [floci](https://github.com/floci-io/floci), a local AWS
emulator, and checks every route in a browser. The harness lives in `tools/floci`; the checks in `web/e2e-floci`.

## Run it

```
make floci-selfcheck                 # safety guards, no containers needed
make floci-up floci-seed floci-check floci-down
```

- `floci-up` starts `floci/floci:2.4.0` (port 4566) and `postgres:16.4-alpine` (port 55433), both bound to 127.0.0.1.
  No Docker socket is mounted; ECS runs in mock mode.
- `floci-seed` builds the estate: three regions, clusters and services, an access role, scaling, stopped tasks,
  logs and Container Insights metrics. It is idempotent.
- `floci-check` builds the binary, migrates the database, creates the `admin` user and runs
  `web/playwright.floci.config.ts`. Its `setup` project runs `tools/floci/connect.sh` (connects the account, tags,
  user `omer`, one restart and one desired-count change on `notifications`) and signs in.
- `floci-down` removes both containers and the generated password and session files.
- `make floci-serve` runs the server in the foreground on port 8482 (`HM_FLOCI_PORT`) if you want to browse it; run
  `tools/floci/connect.sh` from another shell. `HM_FLOCI_REUSE=1` lets Playwright use that running server.
- `HM_E2E_SCREENSHOT_DIR=<dir>` keeps `<route>-<width>-<theme>.png` for every route check.
- `HM_FLOCI_UNFIXME=1` enforces the `known-wave0.ts` waivers and the `focused.spec.ts` fixmes (what the lead sets to verify a lane locally).

## Safety

- The AWS CLI and server only ever see `tools/floci/aws-config` and `aws-credentials` (profile `floci`, dummy keys,
  `endpoint_url = http://127.0.0.1:4566`). `tools/floci/env.sh` exits 2 when `AWS_ENDPOINT_URL` is not
  `127.0.0.1`/`localhost` or `AWS_PROFILE` is not `floci`, and every script sources it.
- The admin password is random per run, written to `tools/floci/.admin-password` (git-ignored, mode 600) and replaced
  after the forced first-sign-in change. It is never printed. The Postgres password is a test-only value.

## What fails on purpose (`web/e2e-floci/allowlist.ts`)

| Call | Why floci cannot serve it |
| --- | --- |
| `GET /api/services/*/logs/tail` | no `StartLiveTail`; the Logs tab shows "Live tail disconnected." with Resume |
| `GET /api/tasks/*/exec` | no SSM sessions; Terminal shows layout only |

The `deploy-settings` read is no longer allowlisted: it returns a partial response on floci, not an HTTP failure.

Also expected, and not HTTP failures:

- Images are not in ECR. Task definitions use plain image strings, so the ECR tag listing has nothing to show.
- There are no CloudTrail-sourced history rows; the harness seeds none.
- `edge-proxy` has Container Insights off on purpose (`tools/floci/seed.sh`), and no metrics are written for its services.

## Layout waivers

`web/e2e-floci/known-wave0.ts` holds the layout-check waivers. `WAVE0_FIXME` is empty, so every route check is enforced.

## Troubleshooting

- `admin sign-in failed`: the database was recreated but the password file is stale; run `make floci-down` first.
- Port 4566, 55433 or 8482 busy: another harness is running; stop it first.
- `docker compose` needs Compose v2.
