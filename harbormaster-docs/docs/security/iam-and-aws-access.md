# IAM and AWS access

This page lists how Harbormaster reaches your AWS accounts and exactly which actions each role grants. It is for the people who review IAM before connecting an account. The source is `terraform/modules/harbormaster-access` and `terraform/modules/harbormaster/iam.tf`.

## How access works

Harbormaster runs in one account and manages ECS in others by assuming a role in each connected account.

1. The Harbormaster task role is allowed only `sts:AssumeRole` on the role ARNs in `access_role_arns`. It has no other AWS permissions of its own, apart from an optional single-secret read for the bootstrap admin password.
2. In each target account, the `harbormaster-access` module creates a role whose trust policy names the Harbormaster task role and requires a matching external ID (`sts:ExternalId`).
3. Each connected account stores its role ARN and external ID. Harbormaster assumes the role for that account and works in the regions you choose. Sessions last up to one hour.

The role ARN and external ID are set when a global admin connects an account. See [Connect AWS accounts](../install/connect-aws-accounts.md) and the [access module reference](../reference/terraform-access.md).

## Roles in each target account

| Role | Created when | Trusted principal | Purpose |
|---|---|---|---|
| `harbormaster-access` (`role_name`) | Always | Single-service task role and/or web task role | API, terminal, deploys |
| `<role_name>-poller` | `poller_task_role_arn` set | Poller task role | Read-only polling |
| `<role_name>-coordinator` | `coordinator_task_role_arn` set | Coordinator task role | Off-hours and scan support |
| `harbormaster-scan` (`scan_role_name`) | `scanner_task_role_arn` set | Scanner task role | Pull images from ECR |

All of them require the external ID.

## Read actions

Granted by the read policy on `harbormaster-access`, on all resources:

| Service | Actions |
|---|---|
| ECS | `ecs:List*`, `ecs:Describe*`, `ecs:ListTagsForResource` |
| EC2 | `DescribeVpcs`, `DescribeSubnets`, `DescribeSecurityGroups`, `DescribeNatGateways`, `DescribeInternetGateways`, `DescribeVpcEndpoints`, `DescribeNetworkInterfaces` |
| EFS | `DescribeFileSystems`, `DescribeAccessPoints`, `DescribeMountTargets` |
| ELB | `DescribeTargetGroups`, `DescribeTargetHealth` |
| Application Auto Scaling | `Describe*` |
| CloudWatch | `GetMetricData` |
| CloudWatch Logs | `FilterLogEvents`, `GetLogEvents`, `StartLiveTail`, `StopLiveTail`, `StartQuery`, `GetQueryResults`, `StopQuery`, `DescribeLogGroups` |
| ECR | `DescribeImages`, `DescribeRepositories`, `BatchGetImage` |

## Write actions

Granted by the write policy, which is created only when `read_only = false` (the default):

| Statement | Actions | Scope |
|---|---|---|
| ECS operations | `ecs:RegisterTaskDefinition`, `ecs:TagResource`, `ecs:UpdateService`, `ecs:StopTask`, `ecs:ExecuteCommand`, `ssm:TerminateSession` | `*` |
| Pass role | `iam:PassRole` | Only `pass_role_arns`, only to `ecs-tasks.amazonaws.com`. Default patterns: `role/*-task` and `role/ecs-task-execution` in the account |
| Auto scaling | `RegisterScalableTarget`, `DeregisterScalableTarget`, `PutScalingPolicy`, `DeleteScalingPolicy`, `PutScheduledAction`, `DeleteScheduledAction` | Condition: `application-autoscaling:service-namespace = ecs` |
| Target-tracking alarms | `cloudwatch:PutMetricAlarm`, `cloudwatch:DeleteAlarms` | Alarms named `TargetTracking-service/*` in the account |
| Alarm lookup | `cloudwatch:DescribeAlarms` | `*` |
| Service-linked role | `iam:CreateServiceLinkedRole` | The Application Auto Scaling ECS service-linked role only |

Harbormaster does not need, and the role does not have, permission to read secret values. Secret references in task definitions are shown as references only.

## Split-service roles

With split services, the poller and coordinator assume narrower roles. The Go test `TestAccessPolicies_CoverEachRolesCallsAndNothingBroader` ties each action list to the AWS calls the code makes.

- Poller: the ECS, EC2, EFS, ELB and Application Auto Scaling describe actions above. No writes.
- Coordinator: `ecs:DescribeServices`, `application-autoscaling:Describe*`, `ecr:BatchGetImage`, `ecr:DescribeImages`, `cloudwatch:GetMetricData`. Unless `read_only = true`, it also gets `ecs:UpdateService`, the scheduled-action and scalable-target writes used by off-hours, and the service-linked role statement.
- Scan role: `ecr:GetAuthorizationToken` on `*`, and `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`, `ecr:DescribeImages` on `scan_repository_arns` (all repositories in the account by default). It is created whatever `read_only` is.

## What the write set lets Harbormaster do

- Deploy: register task definitions and update services.
- Restart: stop tasks or force a new deployment.
- Scale, change rollout settings and manage Application Auto Scaling.
- Turn ECS Exec on or off and open terminal sessions.
- Run off-hours schedules.

## Least-privilege tips

- Set `read_only = true` in accounts where Harbormaster should only observe. The write policy is not created, and deploy, scale, exec and off-hours actions fail at AWS.
- Narrow `pass_role_arns` to the exact task and execution role ARNs your services use. The defaults are name patterns.
- Narrow `scan_repository_arns` to the repositories you scan.
- Keep `access_role_arns` on the Harbormaster task role to the exact roles you connect, not a wildcard.
- Use a long random external ID per account and store it in your Terraform state backend, not in code.
- Services you manage still need their own task roles. Terminal needs `ssmmessages:CreateControlChannel`, `CreateDataChannel`, `OpenControlChannel` and `OpenDataChannel` on the task role, and the task execution role needs `secretsmanager:GetSecretValue`, `ssm:GetParameters` and `kms:Decrypt` for the secrets a container uses.

## Related pages

- [Security model](security-model.md)
- [Hardening checklist](hardening.md)
- [Connect AWS accounts](../install/connect-aws-accounts.md)
