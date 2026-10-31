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

## Read-only mode

With `read_only = true` only `harbormaster-access-read` is created. Harbormaster can inspect services, networking, logs and metrics, but cannot register task definitions, update services, stop tasks, open exec sessions or pass roles. `pass_role_arns` is then unused.
