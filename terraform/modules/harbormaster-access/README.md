# harbormaster-access

IAM roles that Harbormaster assumes in each target AWS account. The trust policies admit only the Harbormaster ECS task roles and require an external ID; attached inline policies grant read access for inspection and (unless `read_only`) the narrow write actions Harbormaster needs.

There are up to three roles per account, all named from `role_name`: the access role itself (used by the single service and by web), `<role_name>-poller` and `<role_name>-coordinator`. With split services the poller assumes `<role_name>-poller` and the coordinator `<role_name>-coordinator`; Harbormaster derives both ARNs from the one ARN stored for the account, so the accounts table and the UI do not change. The poller role can only read; the coordinator role has only what its background jobs need.

## Flow

1. Get the task role ARNs from the `harbormaster` module outputs (`task_role_arn` for the single service; `web_task_role_arn`, `poller_task_role_arn` and `coordinator_task_role_arn` for split services). Apply the `harbormaster` module first: IAM rejects a trust policy that names a role that does not exist.
2. In the Harbormaster UI open *AWS accounts > Connect account* and copy the generated external ID (24 characters).
3. Apply this module in the target account with your Terraform workflow, passing both values.
4. Back in the UI, enter the `role_arn` output and press *Test and connect*.

To move an account to split services, pass the three new ARNs as well, keeping `harbormaster_task_role_arn` until the legacy service is gone (see the upgrade runbook in the `harbormaster` module README). Once it is removed, set `harbormaster_task_role_arn = null`.

Optional: pass `scanner_task_role_arn` from the harbormaster module output to also create the scan role described under *Image scanning*.

```hcl
module "harbormaster_access" {
  source = "./modules/harbormaster-access"

  harbormaster_task_role_arn = "arn:aws:iam::111111111111:role/harbormaster-server-task"
  external_id                = "<value copied from Harbormaster>"

  # Split services (optional):
  # web_task_role_arn         = "arn:aws:iam::111111111111:role/harbormaster-web-task"
  # poller_task_role_arn      = "arn:aws:iam::111111111111:role/harbormaster-poller-task"
  # coordinator_task_role_arn = "arn:aws:iam::111111111111:role/harbormaster-coordinator-task"
}
```

## Inputs

| Name | Type | Default | Notes |
|---|---|---|---|
| `harbormaster_task_role_arn` | string | `null` | The legacy single-service task role. Null, or must match `^arn:aws:iam::[0-9]{12}:role/`. At least one of this and `web_task_role_arn` is required (precondition) |
| `web_task_role_arn` | string | `null` | The web task role; trusted by the access role next to the legacy role. Same format |
| `poller_task_role_arn` | string | `null` | Setting it creates `<role_name>-poller`. Same format |
| `coordinator_task_role_arn` | string | `null` | Setting it creates `<role_name>-coordinator`. Same format |
| `external_id` | string | required | 2-1224 chars from `[\w+=,.@:/-]` |
| `role_name` | string | `"harbormaster-access"` | Lowercase letters, digits, hyphens; max 64 |
| `pass_role_arns` | list(string) | `null` | Null resolves to `arn:aws:iam::<this account>:role/*-task` and `arn:aws:iam::<this account>:role/ecs-task-execution` |
| `scanner_task_role_arn` | string | `null` | Scanner ECS task role allowed to assume the scan role. Null creates no scan role. Otherwise must match `^arn:aws:iam::[0-9]{12}:role/` |
| `scan_role_name` | string | `"harbormaster-scan"` | Lowercase letters, digits, hyphens; max 64. Used only when `scanner_task_role_arn` is set |
| `scan_repository_arns` | list(string) | `null` | ECR repository ARN patterns the scan role may pull from. Null resolves to `arn:aws:ecr:*:<this account>:repository/*` |
| `read_only` | bool | `false` | `true` omits the write policy entirely, and the write statements of the coordinator role |
| `tags` | map(string) | `{}` | Applied to the role (and to the scan role, when created) |

## Outputs

| Name | Description |
|---|---|
| `role_arn` | ARN to enter in Harbormaster |
| `role_name` | Name of the role |
| `poller_role_arn` | ARN of the poller role; null when `poller_task_role_arn` is not set. Nothing to enter: Harbormaster derives it |
| `coordinator_role_arn` | ARN of the coordinator role; null when `coordinator_task_role_arn` is not set. Nothing to enter: Harbormaster derives it |
| `scan_role_arn` | ARN of the scan role; null when `scanner_task_role_arn` is not set |

## Permissions

Every statement is in `main.tf`; this table lists the same actions and conditions.

| Policy | Statement | Actions | Resource | Condition |
|---|---|---|---|---|
| `harbormaster-access-read` | `ReadOnlyInspection` | `ecs:List*`, `ecs:Describe*`, `ecs:ListTagsForResource`, `ec2:Describe{Vpcs,Subnets,SecurityGroups,NatGateways,InternetGateways,VpcEndpoints,NetworkInterfaces}`, `elasticfilesystem:Describe{FileSystems,AccessPoints,MountTargets}`, `elasticloadbalancing:Describe{TargetGroups,TargetHealth}`, `application-autoscaling:Describe*`, `cloudwatch:GetMetricData`, `logs:{FilterLogEvents,GetLogEvents,StartLiveTail,StopLiveTail,StartQuery,GetQueryResults,StopQuery}`, `ecr:{DescribeImages,DescribeRepositories,BatchGetImage}` | `*` | none |
| `harbormaster-access-write` (omitted when `read_only`) | `EcsOperations` | `ecs:RegisterTaskDefinition`, `ecs:TagResource`, `ecs:UpdateService`, `ecs:StopTask`, `ecs:ExecuteCommand`, `ssm:TerminateSession` | `*` | none |
| | `PassTaskRolesToEcs` | `iam:PassRole` | `pass_role_arns` | `iam:PassedToService` = `ecs-tasks.amazonaws.com` |
| | `AutoScalingWrites` | `application-autoscaling:RegisterScalableTarget`, `DeregisterScalableTarget`, `PutScalingPolicy`, `DeleteScalingPolicy`, `PutScheduledAction`, `DeleteScheduledAction` | `*` | `application-autoscaling:service-namespace` = `ecs` |
| | `TargetTrackingAlarms` | `cloudwatch:PutMetricAlarm`, `cloudwatch:DeleteAlarms` | `arn:aws:cloudwatch:*:<this account>:alarm:TargetTracking-service/*` | none |
| | `TargetTrackingAlarmsDescribe` | `cloudwatch:DescribeAlarms` | `*` | none |
| | `AutoScalingServiceLinkedRole` | `iam:CreateServiceLinkedRole` | `arn:aws:iam::<this account>:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService` | `iam:AWSServiceName` = `ecs.application-autoscaling.amazonaws.com` |

`ecr:BatchGetImage` returns image manifests, not layers; it is used to read which CPU architectures an image index holds. The server still cannot pull layers or read image configuration: `ecr:GetDownloadUrlForLayer` stays on the `harbormaster-scan` role only.

### Poller and coordinator roles

| Role | Policy | Actions | Resource | Condition |
|---|---|---|---|---|
| `<role_name>-poller` | `harbormaster-access-poller` | `ecs:List*`, `ecs:Describe*`, `ecs:ListTagsForResource`, `ec2:Describe{Vpcs,Subnets,SecurityGroups,NatGateways,InternetGateways,VpcEndpoints,NetworkInterfaces}`, `elasticfilesystem:Describe{FileSystems,AccessPoints,MountTargets}`, `elasticloadbalancing:Describe{TargetGroups,TargetHealth}`, `application-autoscaling:Describe*` | `*` | none |
| `<role_name>-coordinator` | `harbormaster-access-coordinator` | `ecs:DescribeServices`, `application-autoscaling:Describe*`, `ecr:BatchGetImage`, `ecr:DescribeImages`, `cloudwatch:GetMetricData` | `*` | none |
| | `OffHoursRestoreDesiredCount` (omitted when `read_only`) | `ecs:UpdateService` | `*` | none (an ECS request does not carry the autoscaling namespace key) |
| | `OffHoursWrites` (omitted when `read_only`) | `application-autoscaling:RegisterScalableTarget`, `DeregisterScalableTarget`, `PutScheduledAction`, `DeleteScheduledAction` | `*` | `application-autoscaling:service-namespace` = `ecs` |
| | `AutoScalingServiceLinkedRole` (omitted when `read_only`) | `iam:CreateServiceLinkedRole` | `arn:aws:iam::<this account>:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService` | `iam:AWSServiceName` = `ecs.application-autoscaling.amazonaws.com` |

The poller has no write, log, metric or ECR action. The coordinator cannot exec, register task definitions, stop tasks, pass roles or read logs. The action lists sit between `# policy: <name>` and `# end policy` markers in `main.tf`, and the Go test `TestAccessPolicies_CoverEachRolesCallsAndNothingBroader` (in `internal/awsx`) reads them: it runs one full poller pass and the coordinator jobs against the fake AWS and requires every call to be allowed and every coordinator action to be used (the service-linked role statement is the one exception: AWS creates that role on the first `RegisterScalableTarget` in an account that never used ECS autoscaling and fails the call without it, which the fake never does). Change a list and the test tells you what the code still needs.

Notes:

- `ecs:UpdateService` covers ECS Exec on/off, deployment configuration and health check grace period. No extra permission is needed for those.
- `iam:PassRole` is checked at `RegisterTaskDefinition`, when the task and execution roles are attached. Registering a revision with a role outside `pass_role_arns` fails with AccessDenied. A global admin who changes a task or execution role to one outside `pass_role_arns` gets that AccessDenied at register, so widen `pass_role_arns` first.
- `cloudwatch:DescribeAlarms` is in the write policy, so a `read_only` role cannot list alarms.

## Read-only mode

With `read_only = true` only `harbormaster-access-read` is created. Harbormaster can inspect services, networking, logs and metrics, but cannot register task definitions, update services (desired count, deployment, ECS Exec, rollout settings), stop tasks, open exec sessions, change auto scaling or alarms, or pass roles. `pass_role_arns` is then unused.

## Image scanning

Setting `scanner_task_role_arn` creates the `scan_role_name` role (default `harbormaster-scan`). Its trust policy admits only the scanner ECS task role, never the Harbormaster server role, and requires the same external ID. The scanner task assumes it to pull images from ECR for Syft. The role holds only read actions, so it is created regardless of `read_only`, and `read_only` does not affect it.

The server never holds pull credentials itself, and the scan role has no write action. That is the isolation; it is not a claim that the server gains nothing. A compromised Harbormaster server writes the job manifest (image, role ARN, external ID) and can launch the scanner, so it can direct the scan role to pull any repository in `scan_repository_arns` of any connected account, `read_only` accounts included, and receive package inventories back. Narrow `scan_repository_arns` to the repositories that should be scannable.

| Policy | Statement | Actions | Resource | Condition |
|---|---|---|---|---|
| `harbormaster-scan-pull` | `EcrAuthToken` | `ecr:GetAuthorizationToken` | `*` | none |
| | `EcrPull` | `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`, `ecr:DescribeImages` | `scan_repository_arns` | none |

Tag resolution for requests and the gate uses the existing `ecr:DescribeImages` of `harbormaster-access-read`, so the scan role does not serve it.
