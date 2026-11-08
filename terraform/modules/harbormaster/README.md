# harbormaster module

Deploys Harbormaster on AWS: an ECS Fargate service (`harbormaster-server`) in cluster `harbormaster`, behind an Application Load Balancer with an HTTPS listener (and an optional HTTP-to-HTTPS redirect), backed by a private, encrypted RDS PostgreSQL instance. The module also creates the Secrets Manager secrets (database URL, session key, optional metrics token), the CloudWatch log group, the security groups and the execution and task IAM roles. The task role may only `sts:AssumeRole` into the configured access roles.

## Prerequisites

- A VPC with at least two private subnets in different AZs, plus public subnets unless `internal = true`.
- Outbound access from the private subnets: a NAT gateway, or VPC endpoints for `ecr.api`, `ecr.dkr`, `logs`, `secretsmanager` and `sts` (plus the S3 gateway endpoint for ECR layers).
- An ACM certificate in the same region for the HTTPS listener.
- The Harbormaster image in ECR (or another reachable registry), pinned to a tag. `:latest` is rejected. The image architecture must match `cpu_architecture`.

## Inputs

| Variable | Type | Default | Notes |
|---|---|---|---|
| `vpc_id` | string | required | |
| `private_subnet_ids` | list(string) | required | Service tasks and RDS; two subnets in different AZs are expected; only the count (at least 2) is validated. |
| `public_subnet_ids` | list(string) | `[]` | ALB when `internal = false`; must be non-empty unless `internal` (enforced by a precondition). |
| `internal` | bool | `false` | Internal ALB in `private_subnet_ids`. |
| `allowed_cidrs` | list(string) | `["0.0.0.0/0"]` | ALB ingress. |
| `certificate_arn` | string | required | ACM certificate for the HTTPS listener. |
| `http_redirect` | bool | `true` | Port 80 redirects to 443. |
| `image` | string | required | Full image reference including tag; must not end with `:latest`. |
| `base_url` | string | required | `https://...`, becomes `HM_BASE_URL`. |
| `desired_count` | number | `1` | |
| `cpu` / `memory` | number | `512` / `1024` | Must be a valid Fargate pair. |
| `cpu_architecture` | string | `"X86_64"` | Or `ARM64`; the image must match. |
| `access_role_arns` | list(string) | `["arn:aws:iam::*:role/harbormaster-access"]` | What the task role may assume. Tighten to explicit role ARNs, for example `["arn:aws:iam::111111111111:role/harbormaster-access"]`, so the wildcard account is not trusted. |
| `bootstrap_admin_password_secret_arn` | string | `null` | When set: env var `HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN` and `secretsmanager:GetSecretValue` on exactly this ARN for the task role. |
| `enable_metrics` | bool | `true` | Creates the metrics token secret and injects `HM_METRICS_TOKEN`. |
| `extra_env` | map(string) | `{}` | Additional `HM_*` variables (keys validated). Module-set keys win on collision; `HM_DATABASE_URL`, `HM_SESSION_KEY` and `HM_METRICS_TOKEN` are rejected (injected from Secrets Manager), as are `HM_FAKE_AWS` (demo mode seeds a known-password admin) and `HM_TRUSTED_PROXY` (the module sets it for the ALB). |
| `db_instance_class` | string | `"db.t4g.micro"` | |
| `db_allocated_storage` / `db_max_allocated_storage` | number | `20` / `100` | gp3. |
| `db_engine_version` | string | `"16"` | Major only; minor upgrades are automatic. |
| `db_multi_az` | bool | `false` | |
| `db_deletion_protection` | bool | `true` | |
| `db_kms_key_id` | string | `null` | KMS key for RDS and the secrets; AWS-managed keys when null. Must be a key ARN (aliases and bare key ids are rejected). The execution role gets `kms:Decrypt` on it when set, restricted by `kms:ViaService` to Secrets Manager in the module region. If the bootstrap admin secret is encrypted with this CMK, also grant `kms:Decrypt` on the key to the task role. |
| `log_retention_days` | number | `30` | |
| `alb_idle_timeout` | number | `1800` | Terminal and log-tail WebSockets idle up to 20 minutes. |
| `tags` | map(string) | `{}` | Applied to every taggable resource. |
| `enable_image_scanning` | bool | `false` | Creates the scanner and the server's scan permissions and sets the `HM_SCAN_*` variables. See "Image scanning". |
| `scanner_image` | string | `null` | Scanner image pinned by digest (`...@sha256:<64 hex>`); required when scanning is enabled (precondition on the task definition). |
| `scanner_cpu` / `scanner_memory` | number | `1024` / `2048` | Scanner task size; a valid Fargate pair. |
| `scanner_ephemeral_storage_gib` | number | `40` | `/work` size, 21 to 200. |
| `scan_bucket_name` | string | `null` | Null resolves to `harbormaster-scan-<account id>-<region>`. |
| `scan_role_name` | string | `"harbormaster-scan"` | Role name in each account; becomes `HM_SCAN_ROLE_NAME`. Must match the access module's `scan_role_name`. |
| `scan_role_arns` | list(string) | `null` | What the scanner task role may assume. Null resolves to `arn:aws:iam::*:role/<scan_role_name>`; tighten it like `access_role_arns`. |
| `scanner_egress_cidrs` | list(string) | `["0.0.0.0/0"]` | Scanner egress, tcp 443 only (ECR, S3, STS). |

## Outputs

| Output | Description |
|---|---|
| `task_role_arn` | Task role ARN; the principal to trust in the access-role module. |
| `execution_role_arn` | ECS task execution role ARN. |
| `cluster_name` | ECS cluster name (`harbormaster`). |
| `service_name` | ECS service name (`harbormaster-server`). |
| `alb_dns_name` | ALB DNS name. |
| `alb_zone_id` | ALB hosted zone ID, for Route 53 alias records. |
| `alb_arn` | ALB ARN. |
| `db_address` | RDS hostname. |
| `log_group_name` | `/ecs/harbormaster/harbormaster-server`. |
| `security_group_ids` | Map with keys `alb`, `server`, `db`. |
| `database_url_secret_arn` | ARN of the database URL secret (not its value). |
| `scanner_task_role_arn` | Scanner task role ARN; the principal the access module's scan role trusts. Null when scanning is off. |
| `scan_bucket_name` | Scan bucket name. Null when scanning is off. |

## Image scanning

Off by default. With `enable_image_scanning = true` the module creates a private S3 bucket (SSE-S3, public access blocked, lifecycle expiry of `jobs/` after 7 days, `results/` after 30 and `grype-db/` after 10, which is longer than the server's `HM_SCAN_DB_MAX_AGE` of 120 hours), the `harbormaster-scanner` Fargate task definition, its log group `/ecs/harbormaster/harbormaster-scanner`, a security group with HTTPS egress only and no ingress, and two roles: `harbormaster-scanner-task` and `harbormaster-scanner-execution`. The server task role gains the `harbormaster-server-scan` policy, and the server container gets `HM_SCAN_ENABLED`, `HM_SCAN_CLUSTER_ARN`, `HM_SCAN_TASK_DEFINITION`, `HM_SCAN_SUBNETS`, `HM_SCAN_SECURITY_GROUP`, `HM_SCAN_BUCKET` and `HM_SCAN_ROLE_NAME` (rejected in `extra_env`).

The scanner has its own execution role because the server's execution role can read the database URL and the session key. The scanner container has no `secrets` block and receives only `HM_SCAN_BUCKET` and `HM_SCAN_IMAGE_DIGEST` from the task definition, plus the run id and mode from the server's launch overrides.

**Enablement is two steps.**
1. Apply this module with `enable_image_scanning = true` and `scanner_image` set. Read the `scanner_task_role_arn` output.
2. Apply the `harbormaster-access` module in each account to be scanned with that ARN, which creates the `harbormaster-scan` role the scanner assumes (trusted only by the scanner task role, with the external ID).

**Residual risk (D20).** The server task role can write `jobs/` and `grype-db/`, and the scanner trusts what it reads there, checking only the archive's checksum against the job. A compromised server can therefore feed the scanner a database of its choosing. The scanner cannot reach the server's secrets or the database, writes only `results/`, and everything it writes is parsed with size and shape limits before use.

**Egress.** The server needs HTTPS egress to `grype.anchore.io` (its security group allows all outbound) only when a scan is requested with data older than `HM_SCAN_DB_REFRESH` (24 hours by default). To use an operator mirror instead, write `grype-db/v6/<name>.tar.zst` and `grype-db/v6/latest.json` with `{built_at, checksum, archive_key}` to the bucket and set `HM_SCAN_DB_REFRESH=0` through `extra_env`. Refresh the mirror within 10 days or the lifecycle rule removes it.

**`/work`.** The task mounts a Fargate task volume on `/work` with a read-only root filesystem. `Dockerfile.scanner` creates `/work` owned by uid 65532 (`COPY --chown=65532:65532 ... /work`) and the container runs as that user; Fargate task volumes take the ownership of an existing directory in the image. This has not been run on Fargate yet: the first real scan confirms that the scanner can write to `/work`.

### Permissions

Server task role (`harbormaster-server-scan`):

| Action | Resource | Used for |
|---|---|---|
| `ecs:RunTask` | `task-definition/harbormaster-scanner:*`, condition `ecs:cluster` is the Harbormaster cluster | Starting a scan |
| `ecs:TagResource` | `task/harbormaster/*`, condition `ecs:CreateAction` is `RunTask` | The tag set at launch |
| `ecs:DescribeTasks`, `ecs:StopTask` | `task/harbormaster/*`, same cluster condition | Following a run, stopping a task |
| `iam:PassRole` | the two scanner roles, condition `iam:PassedToService` is `ecs-tasks.amazonaws.com` | `RunTask` |
| `s3:PutObject` | `jobs/*`, `grype-db/*` | Job files, database archive (multipart included) |
| `s3:AbortMultipartUpload` | `grype-db/*` | A failed archive upload |
| `s3:GetObject` | `results/*`, `grype-db/v6/latest.json` | Manifests, results, database listing |
| `s3:DeleteObject` | `jobs/*`, `results/*` | Age-out of finished runs |
| `s3:ListBucket` | the bucket | Only so a missing object is 404 and not 403 |

Scanner task role (`harbormaster-scanner-task`): `sts:AssumeRole` on `scan_role_arns`; `s3:GetObject` on `jobs/*` and `grype-db/*`; `s3:PutObject` on `results/*`. Scanner execution role: the AWS managed `AmazonECSTaskExecutionRolePolicy` only.

## First run

On the first boot, with no admin user yet, Harbormaster prints a one-time setup token to its log group `/ecs/harbormaster/harbormaster-server`. Read it there, for example:

```sh
aws logs tail /ecs/harbormaster/harbormaster-server --since 15m --profile <name> --region <region>
```

If `bootstrap_admin_password_secret_arn` is set, the admin is created from that secret instead and no setup token is printed.

## Single sign-on

The module has no SSO inputs. SSO is configured in Harbormaster's Settings page after the module is applied, and it needs `HM_SESSION_KEY`, which the module already injects. Sign-in is browser-mediated: the identity provider posts its response through the user's browser to `{base_url}/api/auth/saml/acs`, so the IdP never calls the ALB directly. An internal ALB works, and `allowed_cidrs` only has to admit your users' networks. The one call the task makes itself is fetching a metadata URL, so the IdP must have a public `https` address, or upload its metadata XML instead. `base_url` is also the SP identity: changing it changes the SP entity ID and ACS URL, so update the IdP. See [Single sign-on (SAML)](../../../README.md#single-sign-on-saml) in the root README.

## Database secret format

`harbormaster/database-url` holds a complete `postgres://harbormaster:<password>@<host>:<port>/harbormaster?sslmode=require` URL, not JSON and not an RDS-managed master secret (those have no `host`, which Harbormaster rejects). ECS injects the whole value as `HM_DATABASE_URL`, so the server uses it as is. Do not point `HM_DATABASE_SECRET_ARN` at an RDS-managed secret.

## Destroying and re-creating

Two fixed names make a destroy followed by a re-create collide:

- The Secrets Manager secrets (`harbormaster/database-url`, `harbormaster/session-key`, `harbormaster/metrics-token`) use the default 30-day recovery window, so re-creating the module within 30 days fails on the names. Either wait, or force-delete them first:
  `aws secretsmanager delete-secret --secret-id harbormaster/database-url --force-delete-without-recovery --profile <name> --region <region>` (repeat per secret).
- The RDS final snapshot identifier is fixed at `harbormaster-final`, so a second destroy fails while the earlier snapshot exists. Delete or rename that snapshot first:
  `aws rds delete-db-snapshot --db-snapshot-identifier harbormaster-final --profile <name> --region <region>`.

`db_deletion_protection` (default `true`) must also be set to `false` and applied before the database can be destroyed.

## Do not manage Harbormaster with itself

Do not connect Harbormaster's own service, cluster or database to Harbormaster's write permissions without `lifecycle` guards (for example `lifecycle { ignore_changes = [task_definition, desired_count, ...] }` on the `aws_ecs_service`, with the full list in section 7.5) in the managing configuration. See design spec section 7.5 (`docs/design/2026-10-31-harbormaster-design.md`).
