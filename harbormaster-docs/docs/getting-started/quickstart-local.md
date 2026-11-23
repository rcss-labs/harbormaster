# Quickstart: run it locally

This page gets Harbormaster running on your machine with a fake AWS estate, so you can explore the web app without an AWS account. It is for evaluating Harbormaster or developing against it.

## Before you begin

You need:

- Docker, for a local PostgreSQL container.
- Go 1.27 and Node 26, to build and run from source.
- A checkout of the Harbormaster repository.

!!! warning "Demo mode creates a known-password admin"
    `--fake-aws` seeds users with a publicly known password. Never use `--fake-aws` or `HM_FAKE_AWS` against a database that holds real users. Harbormaster refuses to seed demo data when users or accounts already exist.

## Steps

1. Start PostgreSQL. This starts (or reuses) a container named `hm-test-pg` and prints a database URL.

    ```sh
    make test-db
    ```

2. Export the URL it printed.

    ```sh
    export HM_TEST_DATABASE_URL='postgres://postgres:hm@127.0.0.1:54329/postgres?sslmode=disable'
    ```

    If the container already existed, `make test-db` prints a hint instead of a URL. Build the URL from `docker port hm-test-pg`.

3. Optional: build the web app so the binary embeds the current front end. This runs `npm ci` and the front-end build.

    ```sh
    make build
    ```

4. Start the server.

    ```sh
    make run
    ```

    `make run` starts `serve --fake-aws` on `http://127.0.0.1:8480`. It migrates the database, seeds demo users, tags and permissions, and serves a fake ECS estate held in memory. No AWS credentials are needed. It sets `HM_COOKIE_SECURE=false` so sign-in works over plain HTTP.

## Sign in

Open <http://127.0.0.1:8480>. All demo users share one password: `harbormaster-e2e-1`.

| User | Access |
| --- | --- |
| `sam` | Global admin |
| `dana` | Full on Dev, Deployer on Staging, View and Logs on Prod |
| `omer` | Operator on Dev, Viewer on Staging |
| `oncall` | View, Logs, Operate and Terminal on Staging and Prod |

Start as `sam` to see everything.

## Take a tour

- **Overview and Clusters:** the demo estate has two accounts and several clusters, such as `core-services` and `staging-web`. Open a cluster, then a service.
- **A service:** try the Metrics, Task definition, Deploy, Tasks, Timeline, Logs and Terminal tabs. The fake estate supports deploys, restarts, scaling and an interactive terminal.
- **Users and permissions, Tags:** sign in as `dana` or `omer` in another browser profile and see how the same estate looks with narrower access.
- **Off-hours** (Settings): two demo schedules exist.
- **Notifications** (Settings): demo routes deliver to an in-process receiver. Open `/fake-notify/received` to see what was sent. Set `HM_FAKE_NOTIFY_DEMO=false` to turn the demo routes off.
- **Single sign-on:** a demo identity provider is served at `/fake-idp/metadata`, so you can try the SAML setup. See [Single sign-on](../install/single-sign-on.md).
- **Audit log:** sign-ins and changes are recorded.

See [Navigating the web app](../guides/navigating.md) for a guided walk.

## Check it worked

- The terminal running `make run` logs a line saying demo data is loaded and naming the sign-in.
- <http://127.0.0.1:8480/healthz> answers `ok`.

## Stop and clean up

Press Ctrl-C to stop the server. To remove the database container:

```sh
docker rm -f hm-test-pg
```

## Other local modes

- `make run-split` runs the web, poller and coordinator roles as three processes. It needs a Valkey or Redis store (`make test-kv`). See [Dev environment](../development/dev-environment.md).
- `make run-mcp` runs the MCP gateway against a local `make run` that has `HM_API_TOKENS=true`. See [AI access with MCP](../guides/ai-access-mcp.md).

## Next steps

- [Concepts](concepts.md)
- [Quickstart: deploy on AWS](quickstart-aws.md)
- [Dev environment](../development/dev-environment.md)
