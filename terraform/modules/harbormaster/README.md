# harbormaster module

Deploys Harbormaster on AWS: an ECS Fargate service (`harbormaster-server`) in cluster `harbormaster`, behind an Application Load Balancer with an HTTPS listener (and an optional HTTP-to-HTTPS redirect), backed by a private, encrypted RDS PostgreSQL instance. The module also creates the Secrets Manager secrets (database URL, session key, optional metrics token), the CloudWatch log group, the security groups and the execution and task IAM roles. The task role may only `sts:AssumeRole` into the configured access roles.

The same image can instead run as three ECS services (`harbormaster-web`, `harbormaster-poller`, `harbormaster-coordinator`) with a Valkey or Redis store between them. That is off by default (`split_services = false`, `cache_mode = "none"`), so an install that changes nothing keeps its single service. See [Split services](#split-services), [Cache](#cache) and [Upgrade: single service to split services](#upgrade-single-service-to-split-services).

## Prerequisites

- A VPC with at least two private subnets in different AZs, plus public subnets unless `internal = true`.
- Outbound access from the private subnets: a NAT gateway, or VPC endpoints for `ecr.api`, `ecr.dkr`, `logs`, `secretsmanager` and `sts` (plus the S3 gateway endpoint for ECR layers).
- An ACM certificate in the same region for the HTTPS listener.
- The Harbormaster image in ECR (or another reachable registry), pinned to a tag. `:latest` is rejected. The image architecture must match `cpu_architecture`.
- AWS provider 5.82 or newer (below 7.0). 5.73.0 is the first version whose `aws_elasticache_replication_group` accepts `engine = "valkey"`, and 5.82.0 fixes a perpetual diff (a replacement) on `at_rest_encryption_enabled` for Valkey, which the module sets. Terraform 1.5 or newer.

## Inputs

| Variable | Type | Default | Notes |
|---|---|---|---|
| `vpc_id` | string | required | |
| `private_subnet_ids` | list(string) | required | Service tasks and RDS; two subnets in different AZs are expected; only the count (at least 2) is validated. |
| `public_subnet_ids` | list(string) | `[]` | ALB when `internal = false`; must be non-empty unless `internal` (enforced by a precondition). |
| `internal` | bool | required | Required. `true` puts Harbormaster on an internal ALB in `private_subnet_ids` (recommended; needed for `HM_API_TOKENS`). `false` creates an internet-facing ALB. There is no default, so an existing deployment that relied on the old default (`false`) must now set it explicitly instead of having its load balancer replaced. Any true value of `HM_API_TOKENS` (`true`, `1`, `t`) requires `internal = true`: a precondition fails the plan otherwise, because tokens must reach Harbormaster only through the MCP gateway. |
| `allowed_cidrs` | list(string) | required | Required. CIDR blocks allowed to reach the ALB on 443. Use your VPN or office ranges; `0.0.0.0/0` makes the login page reachable from the whole internet. Must list at least one block. |
| `alb_deletion_protection` | bool | `true` | Deletion protection on the ALB. Before a change that replaces the ALB (for example switching `internal` from `false` to `true`), first apply with `alb_deletion_protection = false`, then make the change, then turn it back on. |
| `alb_ingress_security_group_ids` | list(string) | `[]` | Security groups allowed to reach the ALB on 443 in addition to `allowed_cidrs`. Set it to the `harbormaster-mcp` module's `task_security_group_id`. |
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
| `smtp_password_secret_arn` | string | `null` | When set: container secret `HM_SMTP_PASSWORD` and `secretsmanager:GetSecretValue` on exactly this ARN for the execution role. Email notifications also need `HM_SMTP_HOST`, `HM_SMTP_FROM` and the other `HM_SMTP_*` values through `extra_env`. |
| `extra_env` | map(string) | `{}` | Additional `HM_*` variables (keys validated). Module-set keys win on collision; `HM_DATABASE_URL`, `HM_SESSION_KEY`, `HM_METRICS_TOKEN` and `HM_SMTP_PASSWORD` are rejected (injected from Secrets Manager), as are `HM_FAKE_AWS` (demo mode seeds a known-password admin) and `HM_TRUSTED_PROXY` (the module sets it for the ALB). |
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
| `split_services` | bool | `false` | Create the web, poller and coordinator services. Needs a cache (`cache_mode` `create` or `external`). |
| `keep_legacy_service` | bool | `true` | Keep the single `harbormaster-server` service, its task definition and its two roles. `false` needs `split_services = true`. |
| `web_desired_count` / `web_min_count` / `web_max_count` | number | `2` / `2` / `6` | Each at least 1; desired within min and max. Autoscaling owns the count after the first apply. |
| `poller_desired_count` / `poller_min_count` / `poller_max_count` | number | `2` / `2` / `4` | As for web. |
| `coordinator_desired_count` | number | `2` | One task is active, the others stand by. Fixed. |
| `web_cpu` / `web_memory` | number | `512` / `1024` | Must be a valid Fargate pair. |
| `poller_cpu` / `poller_memory` | number | `512` / `1024` | |
| `coordinator_cpu` / `coordinator_memory` | number | `256` / `512` | |
| `web_requests_per_target` | number | `1000` | Target of `ALBRequestCountPerTarget`: requests per target per minute. |
| `metrics_ingress_security_group_ids` | list(string) | `[]` | Groups allowed to scrape `/metrics` on poller and coordinator tasks. |
| `cache_mode` | string | `"none"` | `"create"` builds ElastiCache, `"external"` uses the URL in `cache_url_secret_arn`, `"none"` uses no store. |
| `cache_engine` | string | `"valkey"` | `"valkey"` or `"redis"`. Used when `cache_mode = "create"`. |
| `cache_engine_version` | string | `null` | Null resolves to `7.2` for `valkey` and `7.1` for `redis`. |
| `cache_node_type` | string | `"cache.t4g.micro"` | |
| `cache_replicas` | number | `0` | 0 to 5. `1` or more adds replicas with Multi-AZ automatic failover. |
| `cache_url_secret_arn` | string | `null` | Required with `cache_mode = "external"`, null otherwise: a Secrets Manager secret holding the full store URL. |
| `cache_alarm_actions` | list(string) | `[]` | Notified when the created cache evicts keys. |
| `allow_split_without_cache` | bool | `false` | Deprecated; must stay `false`. The split roles refuse to start without a store, so `split_services` always needs a cache. |

## Outputs

| Output | Description |
|---|---|
| `task_role_arn` | Legacy single-service task role ARN; input `harbormaster_task_role_arn` of the access module. Null once `keep_legacy_service` is false. |
| `execution_role_arn` | Legacy ECS task execution role ARN. Null once `keep_legacy_service` is false. |
| `web_task_role_arn` / `poller_task_role_arn` / `coordinator_task_role_arn` | Task roles of the split services; inputs of the access module. Always created, whatever `split_services` says. |
| `cluster_name` | ECS cluster name (`harbormaster`). |
| `service_name` | Legacy ECS service name (`harbormaster-server`). Null once `keep_legacy_service` is false. |
| `web_service_name` / `poller_service_name` / `coordinator_service_name` | Split service names. Null unless `split_services` is true. |
| `alb_dns_name` | ALB DNS name. |
| `alb_zone_id` | ALB hosted zone ID, for Route 53 alias records. |
| `alb_arn` | ALB ARN. |
| `db_address` | RDS hostname. |
| `log_group_name` | `/ecs/harbormaster/harbormaster-server`. |
| `security_group_ids` | Map with keys `alb`, `server` (legacy), `web`, `poller`, `coordinator`, `db` and `cache` (null unless `cache_mode = "create"`). |
| `cache_url_secret_arn` | ARN of the secret that holds the store URL (not its value). Null when `cache_mode = "none"`. |
| `cache_primary_endpoint` | Address of the created cache's primary endpoint. Null unless `cache_mode = "create"`. |
| `database_url_secret_arn` | ARN of the database URL secret (not its value). |
| `scanner_task_role_arn` | Scanner task role ARN; the principal the access module's scan role trusts. Null when scanning is off. |
| `scan_bucket_name` | Scan bucket name. Null when scanning is off. |

## Split services

With `split_services = true` the module runs one image in three roles as three ECS services in the `harbormaster` cluster:

| Service | `HM_ROLE` | Count | Behind the ALB | Autoscaling |
|---|---|---|---|---|
| `harbormaster-web` | `web` | `web_*_count` (2, 2, 6) | yes, the only target | `ALBRequestCountPerTarget` at `web_requests_per_target` and average CPU at 60 percent, scale-in cooldown 10 minutes (a scale-in closes WebSockets) |
| `harbormaster-poller` | `poller` | `poller_*_count` (2, 2, 4) | no | average CPU at 60 percent, scale-in cooldown 5 minutes. Lease load is on `/metrics`, not in CloudWatch |
| `harbormaster-coordinator` | `coordinator` | `coordinator_desired_count` (2) | no | none: one task is active, the other waits |

Each service has its own task role, execution role and security group, all created even when `split_services` is false: a managed account can trust a role only once it exists, and an unused role grants nothing. The task role grants `sts:AssumeRole` and nothing else, except web's optional bootstrap secret read and the coordinator's scan launch policy:

| Permission | web | poller | coordinator | legacy |
|---|---|---|---|---|
| `sts:AssumeRole` on `access_role_arns` | yes | | | yes |
| `sts:AssumeRole` on each `access_role_arns` entry with `-poller` appended | | yes | | |
| `sts:AssumeRole` on each `access_role_arns` entry with `-coordinator` appended | | | yes | |
| Read the bootstrap admin secret (when set) | yes | | | yes |
| Scan launch policy (`enable_image_scanning`) | | | yes | yes |

The execution role may read exactly the secrets injected into its container:

| Secret | web | poller | coordinator | legacy |
|---|---|---|---|---|
| Database URL | yes | yes | yes | yes |
| Store URL (`HM_KV_URL`, when a cache is configured) | yes | yes | yes | yes |
| Metrics token (`enable_metrics`) | yes | yes | yes | yes |
| Session key | yes | | yes | yes |
| SMTP password (when set) | yes | | yes | yes |

Poller and coordinator tasks serve only `/healthz` and `/metrics` on the container port. Their security groups take no ingress, except that port from `metrics_ingress_security_group_ids`.

### Database connections

Each task holds a database pool (`HM_DB_MAX_CONNS`; unset means 6 for web, 8 for poller, 4 for coordinator, pgx's default for the legacy service) and, during a rollout, one connection for the upgrade gate. At the maximum counts that is about 76 pool connections plus 6 gate connections, against about 110 on the default `db.t4g.micro`. The default instance class stays `db.t4g.micro` so existing installs do not change; **`db.t4g.small` is recommended** (set `db_instance_class`) whenever `web_max_count` or `poller_max_count` is raised above its default.

## Cache

`cache_mode` chooses where the shared store comes from. Without a store (`"none"`) every limit and live-refresh signal stays per task and nothing else changes; that is the single-service default. The data is disposable: when the store is down, empty or evicted, Harbormaster falls back to Postgres and per-task behaviour, and polling, sign-in and deploys never wait for it.

| Mode | What the module does |
|---|---|
| `"create"` | ElastiCache replication group named `harbormaster`: cluster mode off, `1 + cache_replicas` nodes (Multi-AZ automatic failover when there are replicas), port 6379, TLS and at-rest encryption (with `db_kms_key_id` when set), an auth token from `random_password`, no snapshots, `apply_immediately = false`. A parameter group of family `<engine><major>` (`valkey7`, `redis7`) sets `maxmemory-policy = volatile-lru`: every key Harbormaster writes has a TTL, so only those are evicted. The full `rediss://:<token>@<primary endpoint>:6379/0` URL is stored in the secret `harbormaster/kv-url`; the token is in no task definition and no output |
| `"external"` | Uses the secret in `cache_url_secret_arn` (a full `redis://`, `rediss://`, `valkey://` or `valkeys://` URL with the password) for an endpoint you run. The module creates nothing for it. The three split services and the legacy service must be able to reach it; if the secret uses a customer KMS key other than `db_kms_key_id`, add `kms:Decrypt` for the execution roles yourself |
| `"none"` | No store, no secret, no `HM_KV_URL` |

Engines: `cache_engine = "valkey"` (default, version `7.2`) or `"redis"` (version `7.1`); `cache_engine_version` overrides. Harbormaster uses only a command subset that Valkey 7.2 and Redis 7.0 or newer both support, so either engine works. An alarm per node (`harbormaster-cache-evictions-001` and so on) fires when `Evictions` stay above 0 for 15 minutes: size `cache_node_type` up. Set `cache_alarm_actions` to be told.

The cache security group admits the web, poller and coordinator groups on 6379, and the legacy server group only while `keep_legacy_service` is true. The legacy security group and its database rule stay after the legacy service is removed, so removing the service changes nothing about either.

The poller and coordinator security groups are one keyed resource, `aws_security_group.worker["poller"]` and `["coordinator"]`. An install that already has them sees two `has moved to` lines in its next plan and no change to the groups (the `security_group_ids` output keeps its keys).

## Upgrade: single service to split services

Each numbered step below is one Terraform plan and apply, run with your usual Terraform workflow, in this order. Every change is backward compatible: the legacy single service keeps working until run 4 removes it, and rolling back is the reverse run. The module and image version are the same in every run, except where a run says otherwise.

For each run, "account and region" is where Terraform acts: the account that hosts Harbormaster in `<region>` for the main module, and each managed account for the access module. Fill in the placeholders in your workspace; this page does not know them.

### Run 1: main module, new image, no variable changes

- **Module and where:** `harbormaster`, Harbormaster account `admin`, `<region>`.
- **Variables:** none changed. Only `image` moves to the release that has `--role`.
- **The plan must contain:** the six new roles (`harbormaster-web-task`, `-poller-task`, `-coordinator-task` and the three `-execution` roles) with their policies and policy attachments; three new security groups (`harbormaster-web`, `-poller`, `-coordinator`); `terraform_data.split_checks`; one in-place change to the database security group (its ingress gains the three new groups); the legacy task definition replaced by a new revision with the new image, and the legacy service updated in place to use it; and a `has moved to ...[0]` line for each of: `aws_iam_role.task`, `aws_iam_role.execution`, `aws_iam_role_policy.task_assume`, `aws_iam_role_policy.execution_secrets`, `aws_iam_role_policy_attachment.execution_managed`, `aws_ecs_service.server` and `aws_ecs_task_definition.server`.
- **The plan must not contain:** a replace or destroy of the ECS service, the load balancer, the database, the secrets, or any existing role or security group. A replacement of the service or a role means a `moved` block did not match: stop and ask before applying.
- **After:** `/healthz` answers `ok`; the UI loads and signs in; `harbormaster_lease_slices_owned` on the task equals the number of enabled account x region pairs (new tasks wait behind the shared leader gate until the old leader stops, then claim every slice; expect a gap of one retry plus one tick). Copy the outputs `web_task_role_arn`, `poller_task_role_arn` and `coordinator_task_role_arn` for run 2.

### Run 2: access module, once per managed account

This must come after run 1: IAM rejects a trust policy that names a role that does not exist. Today that is four runs, one per managed account.

- **Module and where:** `harbormaster-access`, each managed account in turn, its region (IAM is global; use the region the workspace is configured for).
- **Variables:** add `web_task_role_arn`, `poller_task_role_arn` and `coordinator_task_role_arn` from run 1's outputs. Keep `harbormaster_task_role_arn` and `external_id` as they are. Keep `role_name` the same as in the other accounts: Harbormaster derives `<role_name>-poller` and `<role_name>-coordinator` from the ARN stored for the account.
- **The plan must contain:** two new roles (`<role_name>-poller`, `<role_name>-coordinator`) with one inline policy each, and an in-place change to the existing role's trust policy (it now also names the web task role). It must not remove or replace anything.
- **After:** both suffixed roles exist in that account, for example `aws iam get-role --role-name harbormaster-access-poller --profile <managed account profile> --region <region>`. Nothing changes in Harbormaster yet.

### Run 3: main module, switch on the split services next to the legacy one

- **Module and where:** `harbormaster`, account `admin`, `<region>`.
- **Variables:** `split_services = true`, `cache_mode = "create"`, `cache_engine = "valkey"` (or `"redis"`; for your own endpoint use `cache_mode = "external"` and `cache_url_secret_arn`), `keep_legacy_service = true` (the default). Consider `db_instance_class = "db.t4g.small"` now (see Database connections).
- **The plan must contain:** the ElastiCache replication group, subnet group and parameter group, the `random_password`, the secret `harbormaster/kv-url` and its version, the cache security group, one evictions alarm, three task definitions and three services, two autoscaling targets and three policies; and these changes to existing resources: the legacy task definition gains the `HM_KV_URL` secret (a new revision, the legacy service updated in place) and the legacy execution role's secrets policy gains the store secret. Creating the cache takes several minutes.
- **The plan must not contain:** a destroy of anything, or a replace of the legacy service, the database or the load balancer.
- **After:** run the same workspace once more: the plan must be empty. (On a provider older than 6.45 a Valkey `engine_version` diff can show up; pin `cache_engine_version` to the full version the cache reports, or use a newer provider.) Then check (design step 4): the sum of `harbormaster_lease_slices_owned` over the poller and legacy tasks equals the number of account x region pairs, with no slice stale on the accounts page; `harbormaster_kv_up` is 1 on every task (legacy included); the `harbormaster-server` target group shows healthy web targets next to the legacy ones; a cluster page shows `Live, every 5 s` (releases with page freshness). Leave it running for a while; both services serve the ALB.

### Run 4: main module, remove the legacy service

- **Module and where:** `harbormaster`, account `admin`, `<region>`.
- **Variables:** `keep_legacy_service = false`.
- **The plan must contain only:** the destroy of the legacy ECS service, task definition, task role, execution role and their policies and attachments, and the in-place removal of the legacy security group from the cache security group's ingress. The legacy task gets SIGTERM, frees its leases, and the pollers and the coordinator claim them within one tick.
- **After:** the sum of `harbormaster_lease_slices_owned` over the pollers equals the number of pairs; exactly one coordinator task reports `harbormaster_coordinator_active` = 1; the target group has only web targets and is healthy; the UI works. Outputs `task_role_arn`, `execution_role_arn` and `service_name` are now null.

### Run 5: access module, once per managed account

- **Module and where:** `harbormaster-access`, each managed account in turn.
- **Variables:** `harbormaster_task_role_arn = null` (the legacy role no longer exists, and a trust policy cannot keep naming a deleted role: the next change to it would fail).
- **The plan must contain only:** an in-place change to the existing role's trust policy (the legacy principal removed; the web task role stays).
- **After:** the account still shows healthy slices; the access role trusts the web task role only.

### Rollback and later releases

- **Rollback** is the reverse run at any point: put `keep_legacy_service = true` back (run 4 undone) and the legacy service returns; set `split_services = false` to remove the split services. Rolling the image back is safe because old tasks cannot lead while a new task holds the shared gate.
- **After undoing run 4, repeat run 2 in every managed account, straight away.** Run 4 destroyed `harbormaster-server-task`; putting `keep_legacy_service = true` back creates a new role with the same name but a new unique id, and IAM trust policies match the id, so managed accounts no longer trust it and the legacy service cannot assume its access roles. Re-apply the access module in each account (the plan shows the trust policy changing in place), with `harbormaster_task_role_arn` set to the legacy role's ARN again if run 5 had already set it to `null`. Until then, web, poller and coordinator keep working; only the legacy service is locked out.
- **Repeating run 3 after a rollback** works: the store URL secret `harbormaster/kv-url` is deleted at once (no recovery window) when the cache is destroyed, so its name is free again. The cache itself is rebuilt empty, which is safe because its data is disposable.
- **Later releases:** roll the coordinator and poller services first and web last. Migrations are additive, so old and new tasks can overlap; cache keys carry a schema version, so two versions never read each other's entries.
- **Missing suffixed roles:** if a managed account lacks `<role_name>-poller` or `<role_name>-coordinator`, that account's polls fail with the AWS error and the page shows the stale banner; the accounts page shows each slice's last poll error. Apply run 2 in that account.

## Image scanning

Off by default. With `enable_image_scanning = true` the module creates a private S3 bucket (SSE-S3, public access blocked, lifecycle expiry of `jobs/` after 7 days, `results/` after 30 and `grype-db/` after 10, which is longer than the server's `HM_SCAN_DB_MAX_AGE` of 120 hours), the `harbormaster-scanner` Fargate task definition, its log group `/ecs/harbormaster/harbormaster-scanner`, a security group with HTTPS egress only and no ingress, and two roles: `harbormaster-scanner-task` and `harbormaster-scanner-execution`. The server task role gains the `harbormaster-server-scan` policy, and the server container gets `HM_SCAN_ENABLED`, `HM_SCAN_CLUSTER_ARN`, `HM_SCAN_TASK_DEFINITION`, `HM_SCAN_SUBNETS`, `HM_SCAN_SECURITY_GROUP`, `HM_SCAN_BUCKET` and `HM_SCAN_ROLE_NAME` (rejected in `extra_env`).

The scanner has its own execution role because the server's execution role can read the database URL and the session key. The scanner container has no `secrets` block and receives only `HM_SCAN_BUCKET` and `HM_SCAN_IMAGE_DIGEST` from the task definition, plus the run id and mode from the server's launch overrides.

**Enablement is two steps.**
1. Apply this module with `enable_image_scanning = true` and `scanner_image` set. Read the `scanner_task_role_arn` output.
2. Apply the `harbormaster-access` module in each account to be scanned with that ARN, which creates the `harbormaster-scan` role the scanner assumes (trusted only by the scanner task role, with the external ID).

**Residual risk (D20).** The server task role can write `jobs/` and `grype-db/`, and the scanner trusts what it reads there, checking only the archive's checksum against the job. A compromised server can therefore feed the scanner a database of its choosing. The scanner cannot reach the server's secrets or the database, writes only `results/`, and everything it writes is parsed with size and shape limits before use.

D20 also accepts risks on the scanner side. The scanner task role holds `s3:PutObject` on all of `results/*` and `s3:GetObject` on all of `jobs/*`, so a compromised scanner task (for example through a hostile image or a Syft or Grype flaw) can overwrite other runs' result objects until the next rescan, and can read the job manifests still in the bucket, which name other accounts' scan role ARNs and external IDs. The impact is limited by: the server parsing every result under hard size and shape limits and never trusting raw scanner output; the scan role being assumable only by the scanner task role with the external ID, with read-only ECR pull on `scan_repository_arns` and no write action; a single scanner task at a time by default (`HM_SCAN_MAX_TASKS`); the server deleting `jobs/` and `results/` objects a day after a run ends (so few manifests are present at once); and the scanner having no access to the database, the session key or the access roles. The server side has one more write grant and handles one more untrusted download (the database archive, checksum-checked). Tighten `scan_role_arns` to explicit role ARNs so a leaked external ID is useful for named accounts only.

A compromised server can also direct scans of any repository in `scan_repository_arns` of any connected account by writing a job manifest and launching the scanner, so narrow `scan_repository_arns` in the access module.

**Egress.** The server needs HTTPS egress to `grype.anchore.io` (its security group allows all outbound) only when a scan is requested with data older than `HM_SCAN_DB_REFRESH` (24 hours by default). To use an operator mirror instead, write `grype-db/v6/<name>.tar.zst` and `grype-db/v6/latest.json` with `{built_at, checksum, archive_key}` to the bucket and set `HM_SCAN_DB_REFRESH=0` through `extra_env`. The `grype-db/` lifecycle expiry is 10 days, so an operator mirror must write a fresh archive and `latest.json` within that window or the lifecycle rule removes them; keep `HM_SCAN_DB_MAX_AGE` below 240 hours so the server never treats an expired archive as usable.

**`/work`.** The task mounts a Fargate task volume on `/work` with a read-only root filesystem. `Dockerfile.scanner` creates `/work` owned by uid 65532 (`COPY --chown=65532:65532 ... /work`) and the container runs as that user. `Dockerfile.scanner` also declares `VOLUME /work`, which is what makes the directory a mount point that carries the image's ownership: the AWS documentation on Fargate bind mounts (Amazon ECS Developer Guide, "Bind mounts", the Fargate section) describes a Fargate bind mount over a path that exists in the image as keeping that directory's data and ownership (a path the image lacks is created root-owned). Read the page again if the platform version changes. This has not been run on Fargate yet: the first real scan confirms that the scanner can write to `/work`.

**Turning scanning off.** Setting `enable_image_scanning = false` destroys the bucket, and the apply fails with `BucketNotEmpty` while it holds objects (up to 30 days of results). Empty it first with a profile for the Harbormaster account, for example `aws s3 rm s3://<scan_bucket_name> --recursive --profile <profile> --region <region>`, then run the apply again.

### Permissions

Server task role (`harbormaster-server-scan`):

| Action | Resource | Used for |
|---|---|---|
| `ecs:RunTask` | `task-definition/harbormaster-scanner:*`, condition `ecs:cluster` is the Harbormaster cluster | Starting a scan |
| `ecs:TagResource` | `task/harbormaster/*`, condition `ecs:CreateAction` is `RunTask` | The tag set at launch |
| `ecs:DescribeTasks` | `task/harbormaster/*`, same cluster condition | Following a run |
| `ecs:StopTask` | `task/harbormaster/*`, same cluster condition and tag `harbormaster:scan-run` present | Stopping a scanner task, never a service task |
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

The module has no SSO inputs. SSO is configured in Harbormaster's Settings page after the module is applied, and it needs `HM_SESSION_KEY`, which the module already injects. Sign-in is browser-mediated: the identity provider posts its response through the user's browser to `{base_url}/api/auth/saml/acs`, so the IdP never calls the ALB directly. An internal ALB works, and `allowed_cidrs` only has to admit your users' networks. The one call the task makes itself is fetching a metadata URL, so the IdP must have a public `https` address, or upload its metadata XML instead. `base_url` is also the SP identity: changing it changes the SP entity ID and ACS URL, so update the IdP. See [Single sign-on (SAML)](../../../harbormaster-docs/docs/install/single-sign-on.md).

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

Do not connect Harbormaster's own service, cluster or database to Harbormaster's write permissions without `lifecycle` guards (for example `lifecycle { ignore_changes = [task_definition, desired_count, ...] }` on the `aws_ecs_service`, with the full list below) in the managing configuration. The recommended list for a managed service is `task_definition`, `desired_count`, `enable_execute_command`, `deployment_circuit_breaker`, `deployment_minimum_healthy_percent`, `deployment_maximum_percent` and `health_check_grace_period_seconds`; autoscaling resources edited from Harbormaster need the same treatment.
