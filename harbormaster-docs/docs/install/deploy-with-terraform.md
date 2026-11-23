# Deploy with Terraform

This page shows how to deploy Harbormaster on AWS with the `harbormaster` Terraform module. It is for the engineer who owns the Harbormaster account's infrastructure.

## Before you begin

- Complete [Prerequisites](prerequisites.md).
- Push the image to ECR (see [Build the image](build-image.md)).
- Have a VPC, two private subnets in different AZs, an ACM certificate in the same region, and your VPN or office CIDR ranges.

Run plan and apply with your usual Terraform workflow. Review the plan before every apply.

## What the module creates

- An ECS Fargate service (`harbormaster-server`) in the cluster `harbormaster`.
- An Application Load Balancer with an HTTPS listener, and an optional HTTP-to-HTTPS redirect.
- A private, encrypted RDS PostgreSQL instance.
- Secrets Manager secrets for the database URL, the session key and (optionally) a metrics token.
- A CloudWatch log group, security groups, and the execution and task IAM roles. The task role may only `sts:AssumeRole` into the access roles you configure.

With `split_services` it creates three services instead, and with `cache_mode = "create"` an ElastiCache store. See [Split services](split-services.md).

## Steps

1. Call the module, pinned to a release tag. Replace the placeholder values.

    ```hcl
    module "harbormaster" {
      source = "github.com/rcss-labs/harbormaster//terraform/modules/harbormaster?ref=v1.0.0"

      vpc_id             = "vpc-0123456789abcdef0"
      private_subnet_ids = ["subnet-0123456789abcdef0", "subnet-0123456789abcdef1"]
      certificate_arn    = "arn:aws:acm:<region>:<account-id>:certificate/<certificate-id>"
      image              = "<account-id>.dkr.ecr.<region>.amazonaws.com/harbormaster:v1.0.0"
      base_url           = "https://harbormaster.example.com"
      internal           = true
      allowed_cidrs      = ["10.0.0.0/8"] # your VPN or office ranges
    }
    ```

    The repository's `terraform/examples/harbormaster/main.tf` shows a complete call, including the commented options for split services and image scanning.

2. Expose the outputs you need.

    ```hcl
    output "task_role_arn" {
      value = module.harbormaster.task_role_arn
    }

    output "alb_dns_name" {
      value = module.harbormaster.alb_dns_name
    }
    ```

3. Run plan, review it, then apply.

4. Create a DNS record for the `base_url` hostname that points at `alb_dns_name`. The module also outputs `alb_zone_id`, for a Route 53 alias record.

5. Continue with [First run](first-run.md).

## Required inputs

These seven inputs have no default:

| Input | Type | Description |
| --- | --- | --- |
| `vpc_id` | string | The VPC. |
| `private_subnet_ids` | list(string) | Subnets for tasks and RDS. Two subnets in different AZs are expected; only the count (at least 2) is validated. |
| `certificate_arn` | string | ACM certificate for the HTTPS listener. |
| `image` | string | Full image reference including the tag. Must not end with `:latest`. |
| `base_url` | string | `https://...` URL users open. Becomes `HM_BASE_URL`. |
| `internal` | bool | `true` puts Harbormaster on an internal ALB in `private_subnet_ids`. `false` creates an internet-facing ALB. |
| `allowed_cidrs` | list(string) | CIDR blocks allowed to reach the ALB on 443. Must list at least one block. |

`public_subnet_ids` is also required unless `internal = true`.

!!! warning "Choose `allowed_cidrs` carefully"
    `0.0.0.0/0` makes the login page reachable from the whole internet. Use your VPN or office ranges.

Common optional inputs:

| Input | Default | Description |
| --- | --- | --- |
| `desired_count` | `1` | Tasks of the single service. |
| `cpu` / `memory` | `512` / `1024` | Task size. Must be a valid Fargate pair. |
| `cpu_architecture` | `X86_64` | Or `ARM64`. The image must match. |
| `access_role_arns` | `["arn:aws:iam::*:role/harbormaster-access"]` | What the task role may assume. See [Connect AWS accounts](connect-aws-accounts.md). |
| `bootstrap_admin_password_secret_arn` | `null` | Create the first admin from a secret. See [First run](first-run.md). |
| `db_instance_class` | `db.t4g.micro` | RDS instance class. |
| `db_multi_az` | `false` | Multi-AZ RDS. |
| `http_redirect` | `true` | Port 80 redirects to 443. |
| `extra_env` | `{}` | Extra `HM_*` variables. The module rejects keys it sets itself. |
| `enable_metrics` | `true` | Create the metrics token and inject `HM_METRICS_TOKEN`. |

The full input and output tables are in the [Terraform reference](../reference/terraform-harbormaster.md).

## Internal load balancer by default

Set `internal = true` unless you have a reason not to. Harbormaster can deploy to and open shells in production tasks in every connected account, so keep it off the public internet. Users reach it over a VPN or your office network.

The module has no default for `internal`. This is deliberate: an existing deployment cannot silently change its load balancer. You must choose.

Access tokens and the MCP gateway need `internal = true`. A plan fails if `HM_API_TOKENS` is on with an internet-facing ALB.

## Deletion protection and replacing the load balancer

`alb_deletion_protection` defaults to `true`. Any change that replaces the ALB, such as switching `internal` from `false` to `true`, is blocked while protection is on.

To change a public load balancer to an internal one:

1. Apply with `alb_deletion_protection = false` and no other change.
2. Change `internal` (and remove `public_subnet_ids` if you no longer need them). Run plan, check that it replaces the ALB, and apply.
3. Point your DNS record at the new `alb_dns_name`.
4. Set `alb_deletion_protection = true` again and apply.

`db_deletion_protection` (default `true`) protects the database the same way. It must be `false` and applied before the database can be destroyed.

## Check it worked

- The apply finishes and the `harbormaster-server` service in the `harbormaster` cluster reaches its desired count.
- `https://<your-host>/healthz` answers `ok` from inside an allowed network.

## Notes

- **Terraform and deploys from Harbormaster.** AWS is the source of truth, and Terraform sees a revision registered through Harbormaster as drift. For services you manage with both, add `ignore_changes = [task_definition, desired_count]` to the `aws_ecs_service`. See [Deploy a change](../guides/deploy-a-change.md).
- **Do not manage Harbormaster with itself.** Do not give Harbormaster write access to its own service, cluster or database without `lifecycle` guards in the managing configuration.
- **Destroy and re-create.** Two fixed names collide on a quick re-create: the Secrets Manager secrets (30-day recovery window) and the RDS final snapshot `harbormaster-final`. See the [Terraform reference](../reference/terraform-harbormaster.md).
- **Database secret format.** `harbormaster/database-url` holds a complete `postgres://` URL, not JSON and not an RDS-managed master secret.

## Next steps

- [First run](first-run.md)
- [Connect AWS accounts](connect-aws-accounts.md)
- [Single sign-on](single-sign-on.md)
