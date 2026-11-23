# Terraform module: harbormaster

The `harbormaster` module runs Harbormaster on AWS: an ECS Fargate cluster, an Application Load Balancer, a private RDS PostgreSQL database, the Secrets Manager secrets and the IAM roles. This page lists every input and output, what the module creates and which permissions it grants. Use it when you write or review the Terraform that deploys Harbormaster.

The module source is `terraform/modules/harbormaster`. For the install steps, see [Deploy with Terraform](../install/deploy-with-terraform.md). To turn on split services, see [Split services](../install/split-services.md).

!!! note "Where plans run"
    Run `terraform plan` and `terraform apply` with your usual Terraform workflow. To check a change locally without touching AWS, run `terraform fmt` and `terraform validate`.

## Requirements

| Item | Requirement |
| --- | --- |
| Terraform | 1.5 or newer |
| AWS provider | 5.82 or newer, below 7.0 (5.73 is the first version that accepts `engine = "valkey"` for ElastiCache; 5.82 fixes a perpetual diff on `at_rest_encryption_enabled` for Valkey) |
| Network | A VPC with at least two private subnets in different AZs, plus public subnets unless `internal = true` |
| Outbound access | A NAT gateway, or VPC endpoints for `ecr.api`, `ecr.dkr`, `logs`, `secretsmanager` and `sts`, plus the S3 gateway endpoint for ECR layers |
| Certificate | An ACM certificate in the same region |
| Image | A Harbormaster image pinned to a tag (`:latest` is rejected) that matches `cpu_architecture` |

## Inputs

A variable with no default is required.

| Name | Type | Default | Required | Description |
| --- | --- | --- | --- | --- |
| `vpc_id` | `string` | none | yes | VPC that hosts Harbormaster. |
| `private_subnet_ids` | `list(string)` | none | yes | Private subnets (at least two AZs) for the ECS service and RDS. Also used by the ALB when internal = true. |
| `public_subnet_ids` | `list(string)` | `[]` | no | Public subnets for the ALB. Required unless internal = true. |
| `internal` | `bool` | none | yes | Required. true puts Harbormaster on an internal ALB in private_subnet_ids (recommended; needed for HM_API_TOKENS). false creates an internet-facing ALB. There is no default, so an existing deployment that relied on the old default (false) must now set it explicitly instead of having its load balancer replaced. |
| `allowed_cidrs` | `list(string)` | none | yes | Required. CIDR blocks allowed to reach the ALB on 443. Use your VPN or office ranges; 0.0.0.0/0 makes the login page reachable from the whole internet. |
| `alb_deletion_protection` | `bool` | `true` | no | Turn on deletion protection for the ALB. |
| `alb_ingress_security_group_ids` | `list(string)` | `[]` | no | Security groups allowed to reach the load balancer on 443 in addition to allowed_cidrs. Set it to the harbormaster-mcp module's task_security_group_id. |
| `certificate_arn` | `string` | none | yes | ACM certificate ARN for the HTTPS listener. |
| `http_redirect` | `bool` | `true` | no | Open port 80 and redirect it to 443. |
| `image` | `string` | none | yes | Full container image reference including tag (for example an ECR URI ending in :v1.0.0). |
| `base_url` | `string` | none | yes | Public base URL of Harbormaster (becomes HM_BASE_URL). |
| `desired_count` | `number` | `1` | no | Number of Harbormaster server tasks. |
| `cpu` | `number` | `512` | no | Fargate task CPU units. |
| `memory` | `number` | `1024` | no | Fargate task memory in MiB. |
| `cpu_architecture` | `string` | `"X86_64"` | no | Fargate CPU architecture. The image must be built for the same architecture. |
| `access_role_arns` | `list(string)` | `["arn:aws:iam::*:role/harbormaster-access"]` | no | Role ARN patterns the task role may assume (sts:AssumeRole). Tighten to specific account roles in production. |
| `bootstrap_admin_password_secret_arn` | `string` | `null` | no | Optional Secrets Manager ARN holding the bootstrap admin password. When set it is injected as an env var and the task role may read exactly this secret. |
| `smtp_password_secret_arn` | `string` | `null` | no | Optional Secrets Manager ARN holding the SMTP password for email notifications. When set it is injected as HM_SMTP_PASSWORD (a container secret, never plain text) and the execution role may read exactly this secret. The other HM_SMTP_* values go through extra_env. |
| `enable_metrics` | `bool` | `true` | no | Create the metrics token secret and inject HM_METRICS_TOKEN. |
| `extra_env` | `map(string)` | `{}` | no | Additional HM_* environment variables (poll intervals, retention, limits). |
| `db_instance_class` | `string` | `"db.t4g.micro"` | no | RDS instance class. |
| `db_allocated_storage` | `number` | `20` | no | Initial RDS storage in GiB (gp3). |
| `db_max_allocated_storage` | `number` | `100` | no | Maximum RDS storage autoscaling limit in GiB. |
| `db_engine_version` | `string` | `"16"` | no | PostgreSQL major version; minor upgrades are applied automatically. |
| `db_multi_az` | `bool` | `false` | no | Run RDS in multiple availability zones. |
| `db_deletion_protection` | `bool` | `true` | no | Enable RDS deletion protection. |
| `db_kms_key_id` | `string` | `null` | no | KMS key ARN (arn:aws:kms:<region>:<account>:key/<id>) for RDS storage and the Secrets Manager secrets. Aliases and bare key ids are rejected: the value is used as an IAM Resource. Null uses the AWS-managed keys. |
| `log_retention_days` | `number` | `30` | no | CloudWatch Logs retention for the server log group. |
| `alb_idle_timeout` | `number` | `1800` | no | ALB idle timeout in seconds; terminal and log-tail WebSockets may idle up to 20 minutes. |
| `tags` | `map(string)` | `{}` | no | Tags applied to every taggable resource. |
| `enable_image_scanning` | `bool` | `false` | no | Creates the vulnerability scanner (task definition, bucket, roles, security group, log group) and the server's scan permissions, and turns scanning on in the server. |
| `scanner_image` | `string` | `null` | no | Scanner image pinned by digest, for example <account>.dkr.ecr.<region>.amazonaws.com/harbormaster-scanner@sha256:<64 hex>. Required when enable_image_scanning is true (checked by a precondition on the task definition). |
| `scanner_cpu` | `number` | `1024` | no | Scanner task CPU units; must be a valid Fargate pair with scanner_memory. |
| `scanner_memory` | `number` | `2048` | no | Scanner task memory in MiB. |
| `scanner_ephemeral_storage_gib` | `number` | `40` | no | Scanner task ephemeral storage in GiB (the /work volume: image layers, Syft and Grype caches, the vulnerability database). |
| `scan_bucket_name` | `string` | `null` | no | Name of the scan bucket. Null resolves to harbormaster-scan-<account id>-<region>. |
| `scan_role_name` | `string` | `"harbormaster-scan"` | no | Name of the per-account role the scanner assumes to read ECR (the harbormaster-access module's scan_role_name). |
| `scan_role_arns` | `list(string)` | `null` | no | Role ARN patterns the scanner task role may assume. Null resolves to arn:aws:iam::*:role/<scan_role_name>. Tighten to explicit role ARNs so the wildcard account is not trusted. |
| `scanner_egress_cidrs` | `list(string)` | `["0.0.0.0/0"]` | no | Destinations the scanner may reach on tcp 443 (ECR, S3 and STS). It never contacts the vulnerability data publisher. |
| `split_services` | `bool` | `false` | no | Create the web, poller and coordinator ECS services next to (or instead of) the single harbormaster-server service. Needs a cache (cache_mode "create" or "external"). |
| `keep_legacy_service` | `bool` | `true` | no | Keep the single harbormaster-server service, its task definition and its two roles. Set to false only after the split services are healthy (runbook run 4). Removing the legacy service also removes its task and execution roles; run the access module once more with harbormaster_task_role_arn = null afterwards. |
| `web_desired_count` | `number` | `2` | no | Initial number of web tasks. Autoscaling owns the count afterwards. |
| `web_min_count` | `number` | `2` | no | Minimum number of web tasks (autoscaling). |
| `web_max_count` | `number` | `6` | no | Maximum number of web tasks (autoscaling). With more than 6, use db.t4g.small or larger (see the README, Database connections). |
| `poller_desired_count` | `number` | `2` | no | Initial number of poller tasks. Autoscaling owns the count afterwards. |
| `poller_min_count` | `number` | `2` | no | Minimum number of poller tasks (autoscaling). |
| `poller_max_count` | `number` | `4` | no | Maximum number of poller tasks (autoscaling). With more than 4, use db.t4g.small or larger (see the README, Database connections). |
| `coordinator_desired_count` | `number` | `2` | no | Number of coordinator tasks: one is active and the others stand by. Fixed, not autoscaled. |
| `web_cpu` | `number` | `512` | no | Fargate CPU units of a web task. |
| `web_memory` | `number` | `1024` | no | Fargate memory in MiB of a web task. |
| `poller_cpu` | `number` | `512` | no | Fargate CPU units of a poller task. |
| `poller_memory` | `number` | `1024` | no | Fargate memory in MiB of a poller task. |
| `coordinator_cpu` | `number` | `256` | no | Fargate CPU units of a coordinator task. |
| `coordinator_memory` | `number` | `512` | no | Fargate memory in MiB of a coordinator task. |
| `web_requests_per_target` | `number` | `1000` | no | Target value of ALBRequestCountPerTarget for web autoscaling: requests per target per minute. |
| `metrics_ingress_security_group_ids` | `list(string)` | `[]` | no | Security groups allowed to scrape /metrics on poller and coordinator tasks (the container port). Web is reached through the ALB; the legacy service is unchanged. |
| `cache_mode` | `string` | `"none"` | no | "create" builds an ElastiCache replication group, "external" uses the endpoint in the secret cache_url_secret_arn (an endpoint you run yourself), "none" uses no store: shared limits and live page refresh stay per task and split_services cannot be used. |
| `cache_engine` | `string` | `"valkey"` | no | Engine of the created cache: valkey or redis. Harbormaster uses a command subset both support (Valkey 7.2 and Redis 7.0 or newer). Only used when cache_mode = "create". |
| `cache_engine_version` | `string` | `null` | no | Engine version of the created cache. Null resolves to 7.2 for valkey and 7.1 for redis. |
| `cache_node_type` | `string` | `"cache.t4g.micro"` | no | ElastiCache node type of the created cache. |
| `cache_replicas` | `number` | `0` | no | Replicas of the created cache. 1 or more adds replicas in other availability zones with Multi-AZ automatic failover. |
| `cache_url_secret_arn` | `string` | `null` | no | For cache_mode = "external": ARN of a Secrets Manager secret whose value is the full store URL (redis://, rediss://, valkey:// or valkeys://, with password). Must be null otherwise. |
| `cache_alarm_actions` | `list(string)` | `[]` | no | ARNs (for example an SNS topic) notified when the created cache evicts keys. Empty creates the alarm without actions. |
| `allow_split_without_cache` | `bool` | `false` | no | Deprecated, must stay false. The web, poller and coordinator roles refuse to start without a store (HM_KV_URL), so split_services always needs a cache_mode of "create" or "external". |

## Outputs

| Name | Description |
| --- | --- |
| `task_role_arn` | ARN of the legacy single-service task role (input harbormaster_task_role_arn of the access-role module). Null once keep_legacy_service is false. |
| `execution_role_arn` | ARN of the legacy single-service ECS task execution role. Null once keep_legacy_service is false. |
| `web_task_role_arn` | ARN of the web task role (input web_task_role_arn of the access-role module). Always created. |
| `poller_task_role_arn` | ARN of the poller task role (input poller_task_role_arn of the access-role module). Always created. |
| `coordinator_task_role_arn` | ARN of the coordinator task role (input coordinator_task_role_arn of the access-role module). Always created. |
| `cluster_name` | ECS cluster name. |
| `service_name` | Name of the legacy single ECS service. Null once keep_legacy_service is false. |
| `web_service_name` | Name of the web ECS service. Null unless split_services is true. |
| `poller_service_name` | Name of the poller ECS service. Null unless split_services is true. |
| `coordinator_service_name` | Name of the coordinator ECS service. Null unless split_services is true. |
| `alb_dns_name` | DNS name of the load balancer. |
| `alb_zone_id` | Route 53 hosted zone ID of the load balancer (for alias records). |
| `alb_arn` | ARN of the load balancer. |
| `db_address` | Hostname of the RDS instance. |
| `log_group_name` | CloudWatch log group of the server. |
| `security_group_ids` | Security group IDs keyed alb, server (legacy single service), web, poller, coordinator, db and cache (null unless cache_mode = "create"). |
| `cache_url_secret_arn` | ARN of the Secrets Manager secret that holds the store URL (the ARN only, never the value). Null when cache_mode = "none". |
| `cache_primary_endpoint` | Address of the created cache's primary endpoint (no credentials). Null unless cache_mode = "create". |
| `database_url_secret_arn` | ARN of the Secrets Manager secret holding the database URL (the ARN only, not the value). |
| `scanner_task_role_arn` | ARN of the scanner task role, the principal the access module's scan role trusts. Null when image scanning is off. |
| `scan_bucket_name` | Name of the scan bucket. Null when image scanning is off. |

## Resources the module creates

| Group | Resources | Condition |
| --- | --- | --- |
| Cluster and logging | ECS cluster `harbormaster`, CloudWatch log group `/ecs/harbormaster/harbormaster-server` (retention `log_retention_days`) | Always |
| Single service (legacy) | ECS service `harbormaster-server` and its task definition, task role `harbormaster-server-task`, execution role `harbormaster-server-execution` | `keep_legacy_service = true` (default) |
| Split services | ECS services `harbormaster-web`, `harbormaster-poller`, `harbormaster-coordinator` with task definitions; Application Auto Scaling targets and policies for web (request count and CPU) and poller (CPU) | `split_services = true` |
| Roles for the split services | A task role and an execution role per service (`harbormaster-web-task`, `-poller-task`, `-coordinator-task` and the three `-execution` roles) | Always, so managed accounts can trust a role before you switch services on |
| Load balancer | Application Load Balancer, HTTPS listener (443), HTTP listener with redirect, target group `harbormaster-server` with health check `GET /healthz` expecting 200 | HTTP listener only when `http_redirect = true` |
| Database | RDS PostgreSQL instance `harbormaster` (gp3, encrypted, not public, 7 days of automated backups, final snapshot `harbormaster-final`), DB subnet group, random password | Always |
| Secrets | Secrets Manager secrets `harbormaster/database-url`, `harbormaster/session-key` and, when `enable_metrics = true`, `harbormaster/metrics-token` | Always (metrics token conditional) |
| Cache | ElastiCache replication group `harbormaster`, subnet group, parameter group (`maxmemory-policy = volatile-lru`), auth token, secret `harbormaster/kv-url`, one evictions alarm per node | `cache_mode = "create"` |
| Security groups | `alb`, `server` (legacy), `web`, `poller`, `coordinator`, `db`, and `cache` | `cache` only when `cache_mode = "create"` |
| Image scanning | Private S3 bucket (SSE-S3, public access blocked, lifecycle rules), scanner task definition `harbormaster-scanner`, log group, security group, roles `harbormaster-scanner-task` and `harbormaster-scanner-execution`, and the server's scan policy | `enable_image_scanning = true` |

## IAM permissions the module grants

Each service has its own task role and execution role.

**Task roles** (what the Harbormaster process can do):

| Permission | web | poller | coordinator | legacy |
| --- | --- | --- | --- | --- |
| `sts:AssumeRole` on `access_role_arns` | yes | | | yes |
| `sts:AssumeRole` on each `access_role_arns` entry with `-poller` appended to the role name | | yes | | |
| `sts:AssumeRole` on each `access_role_arns` entry with `-coordinator` appended | | | yes | |
| `secretsmanager:GetSecretValue` on the bootstrap admin secret (when `bootstrap_admin_password_secret_arn` is set) | yes | | | yes |
| Scan launch policy (when `enable_image_scanning = true`) | | | yes | yes |

The task roles hold no other permission. They reach the managed AWS accounts only through the access roles; see [Terraform module: harbormaster-access](terraform-access.md).

**Execution roles** (what ECS uses to start the task) have the AWS managed `AmazonECSTaskExecutionRolePolicy` and may read exactly the secrets injected into their container:

| Secret | web | poller | coordinator | legacy |
| --- | --- | --- | --- | --- |
| Database URL | yes | yes | yes | yes |
| Store URL (`HM_KV_URL`, when a cache is configured) | yes | yes | yes | yes |
| Metrics token (`enable_metrics`) | yes | yes | yes | yes |
| Session key | yes | | yes | yes |
| SMTP password (when `smtp_password_secret_arn` is set) | yes | | yes | yes |

When `db_kms_key_id` is set, the execution roles also get `kms:Decrypt` on that key, limited to use through Secrets Manager in the module's region.

**Scan policy** on the server (legacy) and coordinator task roles, when image scanning is on:

| Action | Resource | Used for |
| --- | --- | --- |
| `ecs:RunTask` | task definition `harbormaster-scanner:*`, limited to the Harbormaster cluster | Start a scan |
| `ecs:TagResource` | `task/harbormaster/*`, only on `RunTask` | Tag a scan run |
| `ecs:DescribeTasks` | `task/harbormaster/*` | Follow a run |
| `ecs:StopTask` | `task/harbormaster/*`, only tasks tagged `harbormaster:scan-run` | Stop a scanner task, never a service task |
| `iam:PassRole` | The two scanner roles, passed to `ecs-tasks.amazonaws.com` | `RunTask` |
| `s3:PutObject`, `s3:AbortMultipartUpload` | `jobs/*`, `grype-db/*` | Job files and database archive |
| `s3:GetObject` | `results/*`, `grype-db/v6/latest.json` | Results and database listing |
| `s3:DeleteObject` | `jobs/*`, `results/*` | Age out finished runs |
| `s3:ListBucket` | The scan bucket | Return 404 instead of 403 for a missing object |

The scanner task role may call `sts:AssumeRole` on `scan_role_arns`, read `jobs/*` and `grype-db/*`, and write `results/*`. The scanner execution role has only `AmazonECSTaskExecutionRolePolicy`, and the scanner container has no secrets.

## Minimal usage example

This is the shape of `terraform/examples/harbormaster/main.tf`, with placeholders. Replace every `<...>` value.

```hcl
module "harbormaster" {
  source = "../../modules/harbormaster"

  vpc_id             = "<vpc-id>"
  private_subnet_ids = ["<subnet-a>", "<subnet-b>"]
  public_subnet_ids  = ["<public-subnet-a>", "<public-subnet-b>"]
  certificate_arn    = "<acm-certificate-arn>"
  image              = "<account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:<version>"
  base_url           = "https://harbormaster.example.com"

  # Keep Harbormaster private: reach it over a VPN or from the office.
  internal      = true
  allowed_cidrs = ["10.0.0.0/8"]
}

output "web_task_role_arn" {
  value = module.harbormaster.web_task_role_arn
}
```

Pass the task role outputs to the access module in each AWS account you connect. To switch to split services, set `split_services = true` and `cache_mode`, then follow the ordered runs in [Split services](../install/split-services.md).

## Notes

- **`internal` has no default.** Set it explicitly. `true` is recommended and is required for access tokens (`HM_API_TOKENS`).
- **`extra_env`** accepts `HM_*` variables only. The module refuses `HM_DATABASE_URL`, `HM_SESSION_KEY`, `HM_METRICS_TOKEN`, `HM_SMTP_PASSWORD`, `HM_KV_URL`, `HM_FAKE_AWS` and `HM_TRUSTED_PROXY` (it sets or injects them itself) and the `HM_SCAN_*` launch variables (it derives them from `enable_image_scanning` and the `scan_*` variables). For the available settings, see [Configuration](configuration.md).
- **Database connections.** Each task holds its own pool. Use `db.t4g.small` or larger when you raise `web_max_count` above 6 or `poller_max_count` above 4.
- **Destroy and re-create.** The secrets use the default 30-day recovery window and the RDS final snapshot name is fixed (`harbormaster-final`), so a re-create inside that window collides on both names. Set `db_deletion_protection = false` and apply before you destroy the database.
- **Do not manage Harbormaster with itself.** If you connect Harbormaster's own cluster to Harbormaster, add `lifecycle { ignore_changes = [...] }` for the fields Harbormaster edits on the `aws_ecs_service`.
