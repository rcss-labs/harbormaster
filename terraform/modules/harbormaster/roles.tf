# One task role and one execution role per split service (web, poller, coordinator), so a task holds only what its
# job needs. They are created whatever split_services says: IAM accepts a managed account's trust policy only for a
# principal that exists, so the roles must exist before any access module names them, and before the services that use
# them. A role grants nothing until a task runs with it.

locals {
  service_roles = toset(["web", "poller", "coordinator"])

  # Container secrets by role. The execution role may read exactly these (values of the map), so the matrix of the
  # design's IAM section is written once. Database URL, store URL and metrics token for all three; the session key
  # (notification routes are sealed under it) and the SMTP password only where a task needs them.
  shared_secrets = merge(
    { HM_DATABASE_URL = aws_secretsmanager_secret.database_url.arn },
    local.kv_enabled ? { HM_KV_URL = local.kv_url_secret_arn } : {},
    var.enable_metrics ? { HM_METRICS_TOKEN = aws_secretsmanager_secret.metrics_token[0].arn } : {},
  )
  session_and_mail_secrets = merge(
    { HM_SESSION_KEY = aws_secretsmanager_secret.session_key.arn },
    var.smtp_password_secret_arn == null ? {} : { HM_SMTP_PASSWORD = var.smtp_password_secret_arn },
  )
  service_secrets = {
    web         = merge(local.shared_secrets, local.session_and_mail_secrets)
    poller      = local.shared_secrets
    coordinator = merge(local.shared_secrets, local.session_and_mail_secrets)
  }

  # Which access role each service assumes: web the stored ARN as is, poller and coordinator the same ARN with the
  # suffix the access module gives its role (Harbormaster appends it, awsx.SuffixRoles).
  service_assume_resources = {
    web         = var.access_role_arns
    poller      = [for arn in var.access_role_arns : "${arn}-poller"]
    coordinator = [for arn in var.access_role_arns : "${arn}-coordinator"]
  }
}

# ---- Execution roles: pull the image, write logs, inject the secrets above -----------------------------------------

resource "aws_iam_role" "service_execution" {
  for_each = local.service_roles

  name               = "${local.name}-${each.key}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "service_execution_managed" {
  for_each = local.service_roles

  role       = aws_iam_role.service_execution[each.key].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "service_execution_secrets" {
  for_each = local.service_roles

  statement {
    sid       = "ReadServiceSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = values(local.service_secrets[each.key])
  }

  dynamic "statement" {
    for_each = var.db_kms_key_id == null ? [] : [var.db_kms_key_id]
    content {
      sid       = "DecryptSecrets"
      actions   = ["kms:Decrypt"]
      resources = [statement.value]

      # Decrypt only on behalf of Secrets Manager (ECS injects the secrets), not for direct use.
      condition {
        test     = "StringEquals"
        variable = "kms:ViaService"
        values   = ["secretsmanager.${local.region}.amazonaws.com"]
      }
    }
  }
}

resource "aws_iam_role_policy" "service_execution_secrets" {
  for_each = local.service_roles

  name   = "${local.name}-${each.key}-secrets"
  role   = aws_iam_role.service_execution[each.key].id
  policy = data.aws_iam_policy_document.service_execution_secrets[each.key].json
}

# ---- Task roles: what the process may do ------------------------------------------------------------------------

resource "aws_iam_role" "service_task" {
  for_each = local.service_roles

  name               = "${local.name}-${each.key}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

data "aws_iam_policy_document" "service_task_assume" {
  for_each = local.service_roles

  statement {
    sid       = "AssumeAccessRoles"
    actions   = ["sts:AssumeRole"]
    resources = local.service_assume_resources[each.key]
  }
}

resource "aws_iam_role_policy" "service_task_assume" {
  for_each = local.service_roles

  name   = "${local.name}-${each.key}-assume"
  role   = aws_iam_role.service_task[each.key].id
  policy = data.aws_iam_policy_document.service_task_assume[each.key].json
}

# Only web reads the bootstrap admin secret (it runs on the task that serves the API).
resource "aws_iam_role_policy" "web_bootstrap" {
  count = var.bootstrap_admin_password_secret_arn == null ? 0 : 1

  name   = "${local.name}-web-bootstrap"
  role   = aws_iam_role.service_task["web"].id
  policy = data.aws_iam_policy_document.task_bootstrap[0].json
}

# Only the coordinator launches scans, so the scan launch policy goes on its role and on no other.
resource "aws_iam_role_policy" "coordinator_scan" {
  count = local.scan_enabled ? 1 : 0

  name   = "${local.name}-coordinator-scan"
  role   = aws_iam_role.service_task["coordinator"].id
  policy = data.aws_iam_policy_document.task_scan[0].json
}
