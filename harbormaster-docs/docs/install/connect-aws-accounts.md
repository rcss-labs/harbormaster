# Connect AWS accounts

This page shows how to connect AWS accounts to Harbormaster with the `harbormaster-access` module. It is for global admins and the engineers who own the target accounts.

Harbormaster reads and changes resources in other accounts by assuming an IAM role in each one. The role trusts Harbormaster's task role and requires an external ID.

## Before you begin

- Harbormaster is deployed and you can sign in as a global admin (see [First run](first-run.md)).
- You can apply Terraform in the target account.
- You have the `task_role_arn` output of the `harbormaster` module. With split services, use the `web_task_role_arn`, `poller_task_role_arn` and `coordinator_task_role_arn` outputs too. Apply the `harbormaster` module first: IAM rejects a trust policy that names a role that does not exist.

## Steps

1. Get the `task_role_arn` output of the `harbormaster` module. It is the principal the target role trusts.
2. In Harbormaster, open **AWS accounts > Connect account** and copy the generated external ID (24 characters).
3. In the target account, apply the `harbormaster-access` module with your Terraform workflow, passing the task role ARN and the external ID.

    ```hcl
    module "harbormaster_access" {
      source = "github.com/rcss-labs/harbormaster//terraform/modules/harbormaster-access?ref=v1.0.0"

      harbormaster_task_role_arn = "arn:aws:iam::<harbormaster-account-id>:role/harbormaster-server-task"
      external_id                = "<external-id-from-harbormaster>"
    }

    output "role_arn" {
      value = module.harbormaster_access.role_arn
    }
    ```

4. Back in Harbormaster, enter the `role_arn` output of that module and press **Test and connect**.

## Check it worked

- The account shows as connected on **AWS accounts**, with its regions.
- After the first poll, its clusters appear on **Clusters**.

## Split services: three roles per account

With split services each managed account has three roles from the same `role_name`:

| Role | Used by | Access |
| --- | --- | --- |
| `harbormaster-access` | web (and the single service) | Read, plus the write actions unless `read_only` |
| `harbormaster-access-poller` | poller | Read only |
| `harbormaster-access-coordinator` | coordinator | Off-hours, the ARM64 check and the daily rollups |

Pass the `web_task_role_arn`, `poller_task_role_arn` and `coordinator_task_role_arn` outputs to the access module as well. Harbormaster derives the suffixed ARNs from the one you enter, so there is nothing more to paste. Keep `role_name` the same in every account.

If an account lacks a suffixed role, that account's polls or jobs fail with the AWS error and the page shows the stale banner. Apply the access module in that account.

## Read-only accounts

Set `read_only = true` on the access module to create a role that can inspect resources but cannot register task definitions, update services, change auto scaling or alarms, stop tasks, open exec sessions or pass roles.

Harbormaster cannot change a read-only account. Off-hours schedules show its services as blocked with the read-only reason.

## Same account

The account where Harbormaster runs can also be connected through a role in that same account. Use the same flow: apply `harbormaster-access` in that account with the task role ARN and the external ID. If you have tightened `access_role_arns`, add that role's ARN to the list.

## Tighten `access_role_arns`

The `harbormaster` module's `access_role_arns` defaults to `["arn:aws:iam::*:role/harbormaster-access"]`. That lets the task role assume a `harbormaster-access` role in any account. Replace it with the explicit role ARNs you connect:

```hcl
access_role_arns = ["arn:aws:iam::<account-id>:role/harbormaster-access"]
```

Do this once your accounts are connected. Add a new ARN each time you connect another account.

## Access module inputs

| Name | Default | Description |
| --- | --- | --- |
| `harbormaster_task_role_arn` | `null` | The single-service task role. Set `null` once the legacy service is removed. At least one of this and `web_task_role_arn` is required. |
| `web_task_role_arn` | `null` | The web task role. |
| `poller_task_role_arn` | `null` | Setting it creates `<role_name>-poller`. |
| `coordinator_task_role_arn` | `null` | Setting it creates `<role_name>-coordinator`. |
| `external_id` | required | 2 to 1224 characters. |
| `role_name` | `harbormaster-access` | Lowercase letters, digits and hyphens, up to 64. |
| `pass_role_arns` | `null` | Roles Harbormaster may pass to ECS. `null` resolves to `arn:aws:iam::<this account>:role/*-task` and `.../role/ecs-task-execution`. |
| `scanner_task_role_arn` | `null` | Set it to create the `harbormaster-scan` role for image scanning. |
| `read_only` | `false` | Omit the write policy. |

The full list, outputs and permission tables are in the [access module reference](../reference/terraform-access.md) and [IAM and AWS access](../security/iam-and-aws-access.md).

## Update the access role after an upgrade

A release can add permissions to the access role. Roles created before a permission was added keep working, but one feature shows a per-account message until you re-apply the module in that account:

- Roles created before `ecr:BatchGetImage` was added cannot read image manifests. The ARM64 (Graviton) advisor shows a message for them.
- Roles created before the Logs Insights actions (`logs:StartQuery`, `logs:GetQueryResults`, `logs:StopQuery`) were added cannot run log search. Log search shows a message for that account.

Everything else works without the re-apply.

## Next steps

- [Users, tags and permissions](../guides/users-tags-permissions.md)
- [AWS accounts](../guides/aws-accounts.md)
- [Split services](split-services.md)
