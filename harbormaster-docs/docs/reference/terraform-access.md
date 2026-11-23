# Terraform module: harbormaster-access

The `harbormaster-access` module creates the IAM role that Harbormaster assumes in one AWS account, so it can read and manage ECS there. Run it once in every account you connect. This page lists every input and output, the roles it creates and the permissions each one grants.

The module source is `terraform/modules/harbormaster-access`. For the connection steps, see [Connect AWS accounts](../install/connect-aws-accounts.md). For how access is scoped, see [IAM and AWS access](../security/iam-and-aws-access.md).

!!! note "Where plans run"
    `terraform plan` and `terraform apply` run in your deployment system, not on a workstation. Apply the `harbormaster` module first: IAM rejects a trust policy that names a role that does not exist.

## Requirements

- Terraform 1.5 or newer and AWS provider 5.82 or newer (below 7.0).
- The task role ARNs from the [`harbormaster` module outputs](terraform-harbormaster.md#outputs).
- The external ID that Harbormaster shows in its Connect account form (AWS accounts, Connect account).

## Inputs

At least one of `harbormaster_task_role_arn` and `web_task_role_arn` must be set. The module refuses to create a role that trusts no task role.

| Name | Type | Default | Required | Description |
| --- | --- | --- | --- | --- |
| `harbormaster_task_role_arn` | `string` | `null` | no | ARN of the legacy single Harbormaster ECS task role (output task_role_arn of the harbormaster module) that is allowed to assume the access role. Keep it while the single service runs; set it to null once keep_legacy_service is false in the harbormaster module. At least one of this and web_task_role_arn is required. |
| `web_task_role_arn` | `string` | `null` | no | ARN of the Harbormaster web task role (output web_task_role_arn of the harbormaster module). It is trusted by the access role next to the legacy role. Null (default) for installs that have not split. |
| `poller_task_role_arn` | `string` | `null` | no | ARN of the Harbormaster poller task role (output poller_task_role_arn of the harbormaster module). Setting it creates the <role_name>-poller role. Null (default) creates none. |
| `coordinator_task_role_arn` | `string` | `null` | no | ARN of the Harbormaster coordinator task role (output coordinator_task_role_arn of the harbormaster module). Setting it creates the <role_name>-coordinator role. Null (default) creates none. |
| `scanner_task_role_arn` | `string` | `null` | no | ARN of the Harbormaster scanner ECS task role that may assume the image scan role. Null (default) creates no scan role. |
| `external_id` | `string` | none | yes | External ID required in the trust policy. Copy the generated value from Harbormaster (AWS accounts > Connect account). |
| `role_name` | `string` | `"harbormaster-access"` | no | Name of the IAM role Harbormaster assumes. The poller and coordinator roles are named <role_name>-poller and <role_name>-coordinator; Harbormaster derives their ARNs by appending the suffix, so keep role_name the same in every account that uses them. |
| `pass_role_arns` | `list(string)` | `null` | no | Role ARN patterns Harbormaster may pass to ECS tasks. Null (default) resolves to arn:aws:iam::<this account>:role/*-task and arn:aws:iam::<this account>:role/ecs-task-execution. |
| `scan_role_name` | `string` | `"harbormaster-scan"` | no | Name of the IAM role the scanner task assumes to pull images from ECR. Only used when scanner_task_role_arn is set. |
| `scan_repository_arns` | `list(string)` | `null` | no | ECR repository ARN patterns the scan role may pull from. Null (default) resolves to arn:aws:ecr:*:<this account>:repository/*. |
| `read_only` | `bool` | `false` | no | When true, the write policy is omitted and Harbormaster can only inspect the account. The coordinator role loses its off-hours write statements too. |
| `tags` | `map(string)` | `{}` | no | Tags applied to the IAM role. |

## Outputs

| Name | Description |
| --- | --- |
| `role_arn` | ARN of the access role; paste it into Harbormaster's Connect account form. |
| `role_name` | Name of the access role. |
| `scan_role_arn` | ARN of the image scan role; null when scanner_task_role_arn is not set. |
| `poller_role_arn` | ARN of the poller access role; null when poller_task_role_arn is not set. |
| `coordinator_role_arn` | ARN of the coordinator access role; null when coordinator_task_role_arn is not set. |

## Resources the module creates

| Resource | Name | Created when |
| --- | --- | --- |
| IAM role (access role) | `role_name` (default `harbormaster-access`) | Always |
| Inline policy `harbormaster-access-read` | On the access role | Always |
| Inline policy `harbormaster-access-write` | On the access role | `read_only = false` (default) |
| IAM role (poller) | `<role_name>-poller`, with inline policy `harbormaster-access-poller` | `poller_task_role_arn` is set |
| IAM role (coordinator) | `<role_name>-coordinator`, with inline policy `harbormaster-access-coordinator` | `coordinator_task_role_arn` is set |
| IAM role (image scan) | `scan_role_name` (default `harbormaster-scan`), with inline policy `harbormaster-scan-pull` | `scanner_task_role_arn` is set |

Every role has a maximum session duration of 3600 seconds.

Harbormaster derives the poller and coordinator role ARNs from the access role ARN stored for the account by appending `-poller` or `-coordinator` to the role name. Keep `role_name` the same in every account that uses them.

## Trust

Each role trusts only the task role ARNs you pass, and only when the caller supplies the external ID (`sts:ExternalId` equals `external_id`).

| Role | Trusted principals |
| --- | --- |
| Access role | `harbormaster_task_role_arn` (legacy single service) and `web_task_role_arn` |
| `<role_name>-poller` | `poller_task_role_arn` |
| `<role_name>-coordinator` | `coordinator_task_role_arn` |
| Scan role | `scanner_task_role_arn` |

## IAM permissions granted

### Access role: read policy (`harbormaster-access-read`)

Resource `*`.

| Service | Actions |
| --- | --- |
| ECS | `ecs:List*`, `ecs:Describe*`, `ecs:ListTagsForResource` |
| EC2 | `DescribeVpcs`, `DescribeSubnets`, `DescribeSecurityGroups`, `DescribeNatGateways`, `DescribeInternetGateways`, `DescribeVpcEndpoints`, `DescribeNetworkInterfaces` |
| EFS | `DescribeFileSystems`, `DescribeAccessPoints`, `DescribeMountTargets` |
| Elastic Load Balancing | `DescribeTargetGroups`, `DescribeTargetHealth` |
| Application Auto Scaling | `application-autoscaling:Describe*` |
| CloudWatch | `cloudwatch:GetMetricData` |
| CloudWatch Logs | `FilterLogEvents`, `GetLogEvents`, `StartLiveTail`, `StopLiveTail`, `StartQuery`, `GetQueryResults`, `StopQuery`, `DescribeLogGroups` |
| ECR | `ecr:DescribeImages`, `ecr:DescribeRepositories`, `ecr:BatchGetImage` |

### Access role: write policy (`harbormaster-access-write`)

Omitted when `read_only = true`.

| Statement | Actions | Resource and conditions |
| --- | --- | --- |
| ECS operations | `ecs:RegisterTaskDefinition`, `ecs:TagResource`, `ecs:UpdateService`, `ecs:StopTask`, `ecs:ExecuteCommand`, `ssm:TerminateSession` | `*` |
| Pass task roles | `iam:PassRole` | `pass_role_arns`; only when passed to `ecs-tasks.amazonaws.com` |
| Auto scaling writes | `RegisterScalableTarget`, `DeregisterScalableTarget`, `PutScalingPolicy`, `DeleteScalingPolicy`, `PutScheduledAction`, `DeleteScheduledAction` | `*`; only service namespace `ecs` |
| Target-tracking alarms | `cloudwatch:PutMetricAlarm`, `cloudwatch:DeleteAlarms` | `alarm:TargetTracking-service/*` in this account |
| Alarm describe | `cloudwatch:DescribeAlarms` | `*` |
| Service-linked role | `iam:CreateServiceLinkedRole` | The Application Auto Scaling ECS service-linked role, only for `ecs.application-autoscaling.amazonaws.com` |

### Poller role (`<role_name>-poller`)

Read-only: `ecs:List*`, `ecs:Describe*`, `ecs:ListTagsForResource`, the EC2, EFS and Elastic Load Balancing describe actions listed above and `application-autoscaling:Describe*`.

### Coordinator role (`<role_name>-coordinator`)

| Actions | Notes |
| --- | --- |
| `ecs:DescribeServices`, `application-autoscaling:Describe*`, `ecr:BatchGetImage`, `ecr:DescribeImages`, `cloudwatch:GetMetricData` | Always |
| `ecs:UpdateService` | Off-hours restore of the desired count; omitted when `read_only = true` |
| `RegisterScalableTarget`, `DeregisterScalableTarget`, `PutScheduledAction`, `DeleteScheduledAction` (namespace `ecs`) | Off-hours sleep and wake; omitted when `read_only = true` |
| `iam:CreateServiceLinkedRole` for the Application Auto Scaling ECS role | Omitted when `read_only = true` |

### Scan role (`scan_role_name`)

| Actions | Resource |
| --- | --- |
| `ecr:GetAuthorizationToken` | `*` |
| `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`, `ecr:DescribeImages` | `scan_repository_arns` (default: every repository in this account) |

The scan role is created even when `read_only = true`: it only pulls images.

## Minimal usage example

This is the shape of `terraform/examples/harbormaster-access/main.tf`, with placeholders. Replace every `<...>` value.

```hcl
module "harbormaster_access" {
  source = "../../modules/harbormaster-access"

  harbormaster_task_role_arn = "arn:aws:iam::<harbormaster-account-id>:role/harbormaster-server-task" # null after the legacy service is removed
  web_task_role_arn          = "arn:aws:iam::<harbormaster-account-id>:role/harbormaster-web-task"
  poller_task_role_arn       = "arn:aws:iam::<harbormaster-account-id>:role/harbormaster-poller-task"
  coordinator_task_role_arn  = "arn:aws:iam::<harbormaster-account-id>:role/harbormaster-coordinator-task"
  external_id                = "<external-id-from-harbormaster>"
}

output "role_arn" {
  description = "Paste this into Harbormaster's Connect account form."
  value       = module.harbormaster_access.role_arn
}
```

Paste the `role_arn` output into the Connect account form. The poller and coordinator role ARNs need no pasting.

## Notes

- Use `read_only = true` to let Harbormaster inspect an account without changing anything. Deploys, restarts, scaling edits, terminals and off-hours then fail with an access denied error from AWS.
- Narrow `pass_role_arns` to the exact task and execution roles you deploy. The default patterns are `role/*-task` and `role/ecs-task-execution` in the same account.
- Narrow `scan_repository_arns` if the scanner should pull from a subset of repositories.
