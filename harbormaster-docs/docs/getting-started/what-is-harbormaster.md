# What is Harbormaster

Harbormaster is a web control center for AWS ECS. This page explains what it does, who it is for, and what it deliberately does not do.

![Harbormaster](../assets/main_logo.png)

## What it is

Harbormaster gives a DevOps team one place to see what runs on ECS, how it behaves, and to change it safely. It covers ECS clusters, services and tasks, task definitions, ECR images, EFS file systems, VPCs and CloudWatch, across several AWS accounts and regions.

It is one Go binary that serves an embedded React web app. It runs as an ECS service in your own AWS account, keeps its state in PostgreSQL, and reaches your other accounts by assuming an IAM role in each one.

## Who it is for

- Platform and DevOps engineers who run ECS in more than one account or region.
- Teams that want engineers to deploy, restart, read logs and open a shell without giving each of them broad AWS console access.
- Admins who need a per-environment answer to "who may deploy to Prod?".

## Problems it solves

Day-to-day ECS work usually means jumping between the ECS console, CloudWatch, the EC2 console and the CLI, in several accounts. The console is scattered, and it has no notion of "this person may restart Staging but only read Prod".

Harbormaster puts these tasks on one screen per thing:

- Check why a task stopped, which revision runs, and what the last deployments were.
- Roll a new image, review the change, and watch the rollout.
- Read and search logs, and see metrics next to them.
- Open a shell in a running task.
- See which subnet a task landed in.
- Give each person exactly the access they need, enforced on the server for every request.

## Feature tour

### Overview and clusters

The overview shows cluster usage and service health across every connected account and region. The clusters page lists every cluster, and a cluster page shows its services and capacity.

![Overview](../assets/screens/overview.png)

![Clusters](../assets/screens/clusters.png)

### Services

A service page has tabs for its overview, metrics, task definition, deploy, tasks, timeline, scaling, logs, terminal and operations log.

![Service overview](../assets/screens/service-overview.png)

- **Metrics:** CloudWatch and Container Insights charts.

  ![Service metrics](../assets/screens/service-metrics.png)

- **Task definition:** the task definition JSON, its revisions, and rollback.

  ![Service task definition](../assets/screens/service-task-definition.png)

- **Deploy:** containers, task size, service settings and auto scaling are reviewed as one change set with a reason, followed by live rollout progress.
- **Tasks:** running and stopped tasks of a service.

  ![Service tasks](../assets/screens/service-tasks.png)

- **Timeline:** changes, task stops, alerts, log errors and terminal sessions on one time axis.

  ![Service timeline](../assets/screens/service-timeline.png)

- **Scaling:** desired count and a read-only auto scaling summary.

  ![Service scaling](../assets/screens/service-scaling.png)

- **Logs:** CloudWatch log history and live tail.

  ![Service logs](../assets/screens/service-logs.png)

- **Terminal:** an interactive shell in a running task through ECS Exec.

  ![Service terminal](../assets/screens/service-terminal.png)

- **Operations log:** who changed or opened what, and when.

  ![Service operations log](../assets/screens/service-operations-log.png)

### Search across services

- **Log search** runs one CloudWatch Logs Insights query over the logs of several services.
- **Config search** finds services by image, base image, environment variable or secret name, port, role or log group.

### Network and storage

![Network tree](../assets/screens/network-tree.png)

The network pages show VPCs, subnets and their connections, per account and per region. The storage page shows EFS file systems, access points and mount targets, and which services use them.

![Storage](../assets/screens/storage.png)

### Tasks across the estate

![Tasks](../assets/screens/tasks.png)

### Users, tags and accounts

Global admins manage users, the tags that drive permissions, and the connected AWS accounts. Every sign-in and change is written to the audit log.

![Users and permissions](../assets/screens/users-permissions.png)

![Tags](../assets/screens/tags.png)

![AWS accounts](../assets/screens/aws-accounts.png)

![Audit log](../assets/screens/audit-log.png)

### Further features

- **Image security:** scan container images for known vulnerabilities with Syft and Grype, and gate deploys on the result.
- **Off-hours schedules:** put services that carry an AWS tag to sleep on a weekly grid.
- **Alert notifications:** send failure alerts to Slack, Telegram, PagerDuty, a webhook or email.
- **Access tokens and MCP:** let scripts and AI tools read Harbormaster, read-only, through a small gateway.
- **Single sign-on:** sign users in through a SAML 2.0 identity provider.

## What it is not

- **Not infrastructure as code.** Harbormaster does not create, delete or import clusters, services, EFS file systems, VPCs or other infrastructure. Terraform (or your tool of choice) owns them. See [Deploy with Terraform](../install/deploy-with-terraform.md).
- **Not a source of truth.** AWS is. Harbormaster keeps users, permissions, tags, audit history and a cache that can be rebuilt from AWS. Its tags are internal and are never read from or written to AWS.
- **Not an image builder or registry.** It does not build or push images. It reads ECR and can scan images you already pushed.
- **Not a backup tool.** It does not back up your services or their data.
- **Not for EKS, plain EC2 or Docker hosts.** ECS only. Fargate gets the most features; EC2 launch type tasks are displayed but have no special handling.
- **Not a push system.** Browsers poll the API about every 5 seconds, so data is a few seconds old by design.

## Next steps

- [Concepts](concepts.md): the vocabulary used across the docs.
- [Architecture](architecture.md): how the pieces fit together.
- [Quickstart: run it locally](quickstart-local.md): try it in a few minutes with demo data.
- [Quickstart: deploy on AWS](quickstart-aws.md): the shortest real install.
