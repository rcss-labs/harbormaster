# harbormaster-access

IAM role that Harbormaster assumes in each target AWS account. The trust policy admits only the Harbormaster ECS task role and requires an external ID; attached inline policies grant read access for inspection and (unless `read_only`) the narrow write actions Harbormaster needs.

## Flow

1. Get the task role ARN from the `harbormaster` module output.
2. In the Harbormaster UI open *AWS accounts > Connect account* and copy the generated external ID (24 characters).
3. Apply this module in the target account through TerraDuckTel, passing both values.
4. Back in the UI, enter the `role_arn` output and press *Test and connect*.

```hcl
module "harbormaster_access" {
  source = "./modules/harbormaster-access"

  harbormaster_task_role_arn = "arn:aws:iam::111111111111:role/harbormaster-server-task"
  external_id                = "<value copied from Harbormaster>"
}
```

## Inputs

| Name | Type | Default | Notes |
|---|---|---|---|
| `harbormaster_task_role_arn` | string | required | Must match `^arn:aws:iam::[0-9]{12}:role/` |
| `external_id` | string | required | 2-1224 chars from `[\w+=,.@:/-]` |
| `role_name` | string | `"harbormaster-access"` | Lowercase letters, digits, hyphens; max 64 |
| `pass_role_arns` | list(string) | `null` | Null resolves to `arn:aws:iam::<this account>:role/*-task` and `arn:aws:iam::<this account>:role/ecs-task-execution` |
| `read_only` | bool | `false` | `true` omits the write policy entirely |
| `tags` | map(string) | `{}` | Applied to the role |

## Outputs

| Name | Description |
|---|---|
| `role_arn` | ARN to enter in Harbormaster |
| `role_name` | Name of the role |

## Permissions

Every statement is in `main.tf`; this table lists the same actions and conditions.

| Policy | Statement | Actions | Resource | Condition |
|---|---|---|---|---|
| `harbormaster-access-read` | `ReadOnlyInspection` | `ecs:List*`, `ecs:Describe*`, `ecs:ListTagsForResource`, `ec2:Describe{Vpcs,Subnets,SecurityGroups,NatGateways,InternetGateways,VpcEndpoints,NetworkInterfaces}`, `elasticfilesystem:Describe{FileSystems,AccessPoints,MountTargets}`, `elasticloadbalancing:Describe{TargetGroups,TargetHealth}`, `application-autoscaling:Describe*`, `cloudwatch:GetMetricData`, `logs:{FilterLogEvents,GetLogEvents,StartLiveTail,StopLiveTail}`, `ecr:Describe{Images,Repositories}` | `*` | none |
| `harbormaster-access-write` (omitted when `read_only`) | `EcsOperations` | `ecs:RegisterTaskDefinition`, `ecs:TagResource`, `ecs:UpdateService`, `ecs:StopTask`, `ecs:ExecuteCommand`, `ssm:TerminateSession` | `*` | none |
| | `PassTaskRolesToEcs` | `iam:PassRole` | `pass_role_arns` | `iam:PassedToService` = `ecs-tasks.amazonaws.com` |
| | `AutoScalingWrites` | `application-autoscaling:RegisterScalableTarget`, `DeregisterScalableTarget`, `PutScalingPolicy`, `DeleteScalingPolicy`, `PutScheduledAction`, `DeleteScheduledAction` | `*` | `application-autoscaling:service-namespace` = `ecs` |
| | `TargetTrackingAlarms` | `cloudwatch:PutMetricAlarm`, `cloudwatch:DeleteAlarms` | `arn:aws:cloudwatch:*:<this account>:alarm:TargetTracking-service/*` | none |
| | `TargetTrackingAlarmsDescribe` | `cloudwatch:DescribeAlarms` | `*` | none |
| | `AutoScalingServiceLinkedRole` | `iam:CreateServiceLinkedRole` | `arn:aws:iam::<this account>:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService` | `iam:AWSServiceName` = `ecs.application-autoscaling.amazonaws.com` |

Notes:

- `ecs:UpdateService` covers ECS Exec on/off, deployment configuration and health check grace period. No extra permission is needed for those.
- `iam:PassRole` is checked at `RegisterTaskDefinition`, when the task and execution roles are attached. Registering a revision with a role outside `pass_role_arns` fails with AccessDenied. A global admin who changes a task or execution role to one outside `pass_role_arns` gets that AccessDenied at register, so widen `pass_role_arns` first.
- `cloudwatch:DescribeAlarms` is in the write policy, so a `read_only` role cannot list alarms.

## Read-only mode

With `read_only = true` only `harbormaster-access-read` is created. Harbormaster can inspect services, networking, logs and metrics, but cannot register task definitions, update services (desired count, deployment, ECS Exec, rollout settings), stop tasks, open exec sessions, change auto scaling or alarms, or pass roles. `pass_role_arns` is then unused.
