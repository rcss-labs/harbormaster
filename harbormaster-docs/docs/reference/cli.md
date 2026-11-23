# Command-line reference

Commands, flags and exit codes of the three Harbormaster binaries: `harbormaster`, `harbormaster-mcp` and `harbormaster-scanner`. Use it when you run the binaries directly, write container commands or debug a failing start.

All settings come from environment variables, not flags. See the [configuration reference](configuration.md). The container images set the entry point to the binary, so `docker run <image> <command>` runs a command below.

## harbormaster

The server image runs `harbormaster serve` by default.

```text
usage: harbormaster <command>

commands:
  serve         run the server; --role=all|web|poller|coordinator picks the parts (default all)
  migrate       apply database migrations
  healthcheck   check that the server is healthy
  version       print version information
  create-admin  create an administrator account
  revoke-tokens revoke access tokens, OAuth grants and codes; --all for everyone, --user <name> for one user
```

Run with no command, or with an unknown command, it prints this usage to standard error and exits 2. There is no `--help` flag on the top-level command.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | Any other error. The message goes to standard error as `error: <message>`. |
| `2` | Usage error: no command, unknown command, bad flags or arguments. |
| `3` | `create-admin` only: the user already exists. |

### serve

Runs the server until it receives `SIGINT` or `SIGTERM`, then shuts down gracefully (15 second drain).

```text
harbormaster serve [--fake-aws] [--role=all|web|poller|coordinator]
```

| Flag | Default | Description |
| --- | --- | --- |
| `--fake-aws` | `false` | Serve an in-memory AWS estate with demo data. Same as `HM_FAKE_AWS=true`. Turns image scanning on. Never use in production. |
| `--role` | `all`, or `HM_ROLE` | Which parts this process runs: `all`, `web`, `poller` or `coordinator`. Overrides `HM_ROLE`. |

What `serve` does at start:

1. Loads and validates the environment. Any invalid value exits 1 with a message that names the variable.
2. Refuses to run a split role (`web`, `poller`, `coordinator`) without `HM_KV_URL` (exit 2).
3. Opens the database (from `HM_DATABASE_URL`, or from the secret in `HM_DATABASE_SECRET_ARN`) and applies pending migrations. Every role migrates on start.
4. Starts the parts that the role runs. See the role table in [Architecture](../getting-started/architecture.md).

Roles that serve the API log a one-time setup token when no user exists yet, unless a bootstrap admin secret is set. See [First run](../install/first-run.md).

### migrate

```text
harbormaster migrate
```

Applies pending database migrations and exits. Prints `no migrations to apply`, or `applied migrations: [...]`. It needs only the database settings. `serve` also migrates on start, so running `migrate` first is optional; use it to separate schema changes from a rollout.

### healthcheck

```text
harbormaster healthcheck
```

Sends `GET http://127.0.0.1:<port>/healthz` to the local server, where the port comes from `HM_LISTEN` (default `:8480`). Exits 0 on HTTP 200 and 1 otherwise, with a 2 second timeout. The Docker `HEALTHCHECK` runs this command. See [Health and metrics](../operations/health-and-metrics.md).

### version

```text
harbormaster version
```

Prints `harbormaster <version> (<commit>)`. The values are set at build time. A local build prints `dev` and `none`.

### create-admin

```text
harbormaster create-admin --username <name> [--password-stdin]
```

Creates a break-glass global administrator directly in the database. A break-glass admin can sign in with a password while single sign-on is on. The user must change the password at first sign-in.

| Flag | Description |
| --- | --- |
| `--username` | Required. 2 to 32 characters: lowercase letters, digits, dot, dash or underscore, starting with a letter. |
| `--password-stdin` | Read the password from the first line of standard input. At least 12 characters. Without this flag Harbormaster generates a temporary password and prints it once. |

It needs only the database settings. Exit 3 means the user already exists; exit 2 means the flags were wrong.

### revoke-tokens

```text
harbormaster revoke-tokens --all | --user <username>
```

Deletes access tokens, OAuth grants and pending authorization codes, and writes one audit entry by actor `cli` (`tokens_revoke_all` or `tokens_revoke_user`) with the counts. Use it after an incident. It works whether `HM_API_TOKENS` and `HM_OAUTH` are on or off, and needs only the database settings.

| Flag | Description |
| --- | --- |
| `--all` | Revoke every user's tokens, grants and codes in one transaction. |
| `--user <username>` | Revoke only that user's tokens (and their grants and codes). |

Give exactly one of the two. Neither, or both, prints the usage and exits 2 without deleting anything. See [Access tokens](../guides/access-tokens.md).

## harbormaster-mcp

The read-only MCP gateway. Its image runs `harbormaster-mcp serve`.

```text
usage: harbormaster-mcp <command>

commands:
  serve        serve MCP on HM_MCP_LISTEN, forwarding to HM_MCP_UPSTREAM_URL
  healthcheck  exit 0 when the local gateway answers /healthz, 1 otherwise
  version      print the version and commit
```

`-h`, `--help` and `help` print the usage to standard output and exit 0. No command or an unknown command prints it to standard error and exits 2.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | `serve`: clean shutdown after `SIGINT` or `SIGTERM`. `healthcheck`, `version`, `help`: success. |
| `1` | `serve`: runtime failure (listen failed, server stopped, shutdown incomplete). `healthcheck`: the gateway did not answer 200. |
| `2` | `serve`: configuration error (the log line names the variable). Also usage errors. |

### serve

```text
harbormaster-mcp serve
```

Listens on `HM_MCP_LISTEN` (default `:8481`) and forwards each MCP tool call to `HM_MCP_UPSTREAM_URL` with the caller's own token. It logs JSON to standard error. Shutdown has a 20 second drain. Variables are in the [MCP gateway](configuration.md#mcp-gateway) table.

### healthcheck

```text
harbormaster-mcp healthcheck
```

Sends `GET /healthz` to `127.0.0.1` on the port of `HM_MCP_LISTEN`, with a 3 second timeout and no redirects or proxy. Exit 0 on HTTP 200, otherwise 1.

### version

Prints `harbormaster-mcp <version> <commit>`.

## harbormaster-scanner

The image scan task. The server launches it on ECS for each scan batch; you rarely run it yourself.

```text
usage: harbormaster-scanner <command>

commands:
  run          scan the images of the job named by HM_SCAN_RUN_ID
  healthcheck  check that this binary, syft and grype are executable
  version      print the version
```

`-h`, `--help` and `help` print the usage to standard output and exit 0. No command or an unknown command exits 2.

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success. |
| `1` | `run`: invalid configuration, AWS configuration failed, or the job failed. `healthcheck`: a binary is missing or not executable. |
| `2` | Usage error. |

### run

```text
harbormaster-scanner run
```

Reads the job named by `HM_SCAN_RUN_ID` from the S3 bucket in `HM_SCAN_BUCKET`, scans the listed ECR images with Syft and Grype, and writes the results back to the bucket. It stops cleanly on `SIGINT` or `SIGTERM`. `HM_SCAN_RUN_ID` must be a positive number and `HM_SCAN_BUCKET` is required. Other variables are in [Image security](configuration.md#scanner-task-settings).

### healthcheck

```text
harbormaster-scanner healthcheck
```

Checks that its own executable, `HM_SCAN_SYFT` (default `/usr/local/bin/syft`) and `HM_SCAN_GRYPE` (default `/usr/local/bin/grype`) exist and are executable. Exit 0 if all are; otherwise 1, with the path in the message.

### version

Prints `harbormaster-scanner <version>`.
