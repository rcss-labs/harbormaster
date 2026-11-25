# Changelog

This page records notable changes to Harbormaster. It follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). No release has been tagged yet, so everything is under Unreleased.

## Unreleased

### Added

- Web app for ECS: overview, clusters, services, tasks, task definitions, metrics and health, with live updates for the pages you have open.
- Service operations: deploy a change through a Deploy tab, restart, scale, change rollout settings and roll back, with a history of changes (operations log and timeline).
- Browser terminal in running tasks through ECS Exec, with idle, duration and per-user limits.
- Logs: live tail and history for service containers, and log search across services.
- Config search across task definition names and secret references.
- Tag-based permissions (View, Logs, Operate, Deploy, Edit, Terminal, Accept risk), users, and an audit log.
- Local password sign-in and SAML 2.0 single sign-on, with break-glass admins.
- Access tokens and a read-only MCP gateway for AI tools, with an optional OAuth 2.1 server for connectors.
- Image security scanning with Syft and Grype, a pre-deploy gate, and accepted-risk records.
- Off-hours schedules that put services to sleep and wake them.
- Alert notifications to Slack, Telegram, PagerDuty, webhook and email routes, with quiet hours.
- Automatic AI investigations of alerts: a per-route switch, a read-only agent identity with its own access, a stored summary and evidence, and a Diagnosis message after the Opened message. Off unless the AI agent is set up. See [Automatic AI investigations](../guides/ai-agent-investigator.md).
- Cost analytics, including Fargate Spot advice.
- Networks and storage views.
- Multiple AWS accounts and regions, connected through an assumed role with an external ID.
- Split services: web, poller and coordinator roles with leases, backed by Valkey or Redis, for high availability.
- Terraform modules `harbormaster`, `harbormaster-access` and `harbormaster-mcp`, with examples.
- Prometheus metrics endpoint and alarms.
- Local development with a fake AWS estate, and a floci-based AWS emulator harness.
- Playwright end-to-end, split smoke, soak and Terraform check suites.

### Security

- Server-side authorization on every endpoint and WebSocket, hashed access tokens, sealed stored secrets, and an outbound address guard.
