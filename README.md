# Harbormaster

[![License: MIT](https://img.shields.io/github/license/rcss-labs/harbormaster)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/rcss-labs/harbormaster?logo=go)](go.mod)
[![React 19](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=white)](web/package.json)
[![Terraform >= 1.5](https://img.shields.io/badge/Terraform-%3E%3D1.5-844FBA?logo=terraform&logoColor=white)](terraform/modules)
[![AWS ECS](https://img.shields.io/badge/AWS-ECS-FF9900?logo=amazonwebservices&logoColor=white)](harbormaster-docs/docs/getting-started/what-is-harbormaster.md)
[![MCP](https://img.shields.io/badge/MCP-read--only%20gateway-6E56CF)](harbormaster-docs/docs/guides/ai-access-mcp.md)
[![Docs](https://img.shields.io/badge/docs-harbormaster--docs-0A7BBB)](harbormaster-docs/docs/index.md)
[![Last commit](https://img.shields.io/github/last-commit/rcss-labs/harbormaster)](https://github.com/rcss-labs/harbormaster/commits/main)
[![Open issues](https://img.shields.io/github/issues/rcss-labs/harbormaster)](https://github.com/rcss-labs/harbormaster/issues)

Harbormaster is a web control center for AWS ECS. It gives a DevOps team one place to see what runs across their AWS accounts and regions, how it behaves, and to change it safely: ECS clusters, services and tasks, task definitions, ECR images, EFS file systems, VPCs and CloudWatch.

![Harbormaster](assets/main_logo.png)

**[Read the documentation →](harbormaster-docs/docs/index.md)**

## What it does

- **See the estate**: clusters, services, tasks, networks and storage across every connected account and region
- **Change it safely**: deploys reviewed as one change set with a reason, live rollout progress, and rollback
- **Debug**: logs and live tail, log search across services, config search, metrics, a per-service timeline, and a terminal into running tasks through ECS Exec
- **Control access**: tag-based permissions checked on every request, SAML single sign-on, access tokens, and an audit log
- **Stay ahead of problems**: image vulnerability scanning, off-hours schedules, and failure alerts to Slack, Telegram, PagerDuty, webhooks or email
- **AI access**: a read-only MCP gateway so AI assistants can query the estate with the user's own permissions

Harbormaster is one Go binary serving an embedded React app, backed by Postgres, with optional Valkey or Redis. It reaches AWS by assuming an IAM role in each managed account. See the [architecture](harbormaster-docs/docs/getting-started/architecture.md).

## Try it

On your laptop, with fake AWS data and no credentials (needs Go, Node and Docker):

```sh
make test-db                                     # start a local Postgres and print its URL
export HM_TEST_DATABASE_URL='postgres://...'     # the URL make test-db printed
make run                                         # http://127.0.0.1:8480
```

Sign in as `sam` / `harbormaster-e2e-1`. Details: [Quickstart (local)](harbormaster-docs/docs/getting-started/quickstart-local.md).

To run it in AWS, follow [Quickstart (AWS)](harbormaster-docs/docs/getting-started/quickstart-aws.md) and the [install guide](harbormaster-docs/docs/install/prerequisites.md).

## Documentation

| | |
|---|---|
| [Getting started](harbormaster-docs/docs/getting-started/what-is-harbormaster.md) | Concepts, architecture, quickstarts |
| [Install](harbormaster-docs/docs/install/prerequisites.md) | Image, Terraform, AWS accounts, SSO, split services, MCP gateway, upgrades |
| [User guides](harbormaster-docs/docs/guides/navigating.md) | Deploy, roll back, logs, search, terminal, scaling, image security, alerts, permissions |
| [Operations](harbormaster-docs/docs/operations/health-and-metrics.md) | Health, high availability, throttling, costs, retention, backups, troubleshooting |
| [Reference](harbormaster-docs/docs/reference/configuration.md) | Configuration, CLI, HTTP API, permissions, metrics, Terraform modules |
| [Security](harbormaster-docs/docs/security/security-model.md) | Security model, IAM, hardening checklist |
| [Development](harbormaster-docs/docs/development/dev-environment.md) | Dev setup, testing, conventions, releases |

## Contributing

Contributions are welcome. Read [Contributing](harbormaster-docs/docs/community/contributing.md) and the [code of conduct](harbormaster-docs/docs/community/code-of-conduct.md). For help, see [Support](harbormaster-docs/docs/community/support.md).

Report security issues privately: see [Reporting vulnerabilities](harbormaster-docs/docs/security/reporting-vulnerabilities.md). Don't open a public issue.

## Licence

MIT. See [`LICENSE`](LICENSE) and [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
