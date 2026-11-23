# Testing

This page lists Harbormaster's test suites, what each needs, and how to run them. It is for contributors.

## Expectations

- Fix a bug by writing a focused test that fails for the right reason first, then the fix.
- Add or update a test for new or changed behavior.
- Test observable behavior, not implementation details.
- Documentation, formatting and simple configuration changes need no new test. Run the existing checks instead.
- Run the focused tests while you work and `make lint test` before you open a pull request.

## Suites

| Suite | Command | Needs |
|---|---|---|
| Go unit tests and web unit tests | `make test` | Go and Node. Database-backed tests need Postgres |
| Postgres-backed tests | `make test-db`, export `HM_TEST_DATABASE_URL`, then `make test` | Docker |
| Valkey and Redis tests | `make test-kv`, export `HM_TEST_KV_URLS`, then `make test` | Docker. The tests skip when the variable is unset |
| End-to-end (Playwright) | `make e2e` | `HM_TEST_DATABASE_URL`, `cd web && npx playwright install chromium`, port 8481 free |
| Split smoke | `make split-smoke [ENGINE=valkey\|redis]` | Docker, curl, jq, python3, `HM_TEST_DATABASE_URL`. About two minutes |
| Soak | `make soak` | `HM_TEST_DATABASE_URL`. About 35 minutes |
| Terraform check | `make tf-check` | `terraform` on PATH; `tflint` optional |
| Image checks | `make check-image`, `make check-scanner-image`, `make check-mcp-image` | Docker |
| floci checks | `make floci-check`, `make floci-mcp-check`, `make floci-sanity` | Docker. See [Local AWS with floci](local-aws-with-floci.md) |
| Terminal integration | `make test-it-exec` | Real AWS credentials and `HM_IT_*` variables |

### Details

- **Unit tests.** `make test` runs `go test ./...` with a 30-minute timeout, then Vitest for `web/`.
- **Postgres.** `make test-db` starts `hm-test-pg` (`postgres:16.4-alpine`, port 54329). Tests that need a database skip when `HM_TEST_DATABASE_URL` is unset.
- **KV.** `make test-kv` starts one Valkey and one Redis container at the oldest versions the store layer supports (pinned in `tools/kv-images.txt`). Store tests run against every URL in `HM_TEST_KV_URLS`.
- **End-to-end.** `make e2e` builds the binary, recreates the `hm_e2e` schema, and runs the Playwright specs against `serve --fake-aws`. Failed-run artifacts are copied to `web/test-results-failed/<time>`.
- **Split smoke.** `tools/split-smoke.sh` starts web, two pollers and the coordinator as real processes with a private Valkey or Redis container. It checks health, leases, a kick crossing processes, live focus, the snapshot cache, a poller takeover and a store outage. It exits 1 and names the first check that failed.
- **Soak.** `make soak` runs rate-limit and back-off tests at target estate scale and the split-service soaks (three pollers, cycle time, focus call rates).
- **tf-check.** `tools/tf-check.sh` runs fmt, `init -backend=false`, validate and optional tflint on every Terraform root, and checks each module has an example. It never plans or applies.
- **Integration exec test.** `internal/execbridge/integration_test.go` runs a real shell round trip against a real ECS task and a rejection check when exec is off. It is skipped unless `HM_IT_AWS=1`. Run it only against an account you intend to exercise.

`make test` never calls AWS.

## Continuous integration

GitHub Actions runs `.github/workflows/ci.yml` on every pull request and every push to `main`. Each check is its own job, and they run in parallel.

| Job | Runs | Required to merge |
|---|---|---|
| `lint` | `make lint` | Yes |
| `go-test` | `go test ./...` against Postgres, Valkey and Redis service containers | Yes |
| `web-test` | Vitest (`npm --prefix web test -- --run`) | Yes |
| `tf-check` | `make tf-check` with tflint | Yes |
| `e2e` | `make e2e` (Playwright) | No, reported only |
| `images` | Builds and runtime-checks the server, scanner and MCP images | No, reported only |
| `vulncheck` | `make vulncheck`: fails on called advisories not listed, with a reason, in `tools/vulncheck-accepted.txt` | No, reported only |

- `main` is protected: changes arrive through a pull request with one approval, and the four required checks must pass. Repository administrators can bypass these rules.
- The `go-test` service images are pinned to the same versions as `make test-db` and `tools/kv-images.txt`. The job's first step fails if they drift, so change a pin in both places.
- When `e2e` fails, its screenshots and traces are attached to the run as the `e2e-failures` artifact for 7 days.
- `.github/workflows/soak.yml` runs `make soak` every Monday at 03:00 UTC. Start it by hand from the Actions tab with **Run workflow**.
- Pull requests from forks run with a read-only token and no secrets.
- If a check fails for a reason unrelated to the change, such as a known flaky test, rerun the failed job from the pull request's Checks tab. Report flaky tests as issues.

## Terminal integration test against real AWS

`make test-it-exec` starts a terminal session in a real ECS task, runs an `echo`, runs `exit`, and checks that the session closes as ended by the agent. It makes real ECS and SSM calls. It needs AWS credentials and these variables:

| Variable | Required | Description |
|---|---|---|
| `HM_IT_ROLE_ARN` | yes | The `harbormaster-access` role in the test account |
| `HM_IT_EXTERNAL_ID` | yes | That role's external ID |
| `HM_IT_REGION` | yes | Region of the test task |
| `HM_IT_ACCOUNT_ID` | yes | Account of the test task |
| `HM_IT_CLUSTER` | yes | Cluster of the test task |
| `HM_IT_TASK_ARN` | yes | A running task with ECS Exec enabled |
| `HM_IT_CONTAINER` | no | Container name. Defaults to the task's first container |
| `HM_IT_COMMAND` | no | Shell to start. Defaults to `/bin/sh` |
| `HM_IT_TASK_ARN_NOEXEC` | no | A running task whose service has exec off. Enables the rejection check |

```sh
export AWS_PROFILE=<profile>     # credentials for the test account
export HM_IT_ROLE_ARN='arn:aws:iam::<account-id>:role/harbormaster-access'
export HM_IT_EXTERNAL_ID='<external-id>'
export HM_IT_REGION=<region>
export HM_IT_ACCOUNT_ID='<account-id>'
export HM_IT_CLUSTER='<cluster>'
export HM_IT_TASK_ARN='arn:aws:ecs:<region>:<account-id>:task/<cluster>/<task-id>'
make test-it-exec
```

## Next steps

- [Coding conventions](coding-conventions.md)
- [Frontend](frontend.md)
