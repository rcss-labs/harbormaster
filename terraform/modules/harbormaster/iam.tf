data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Legacy execution role (single service): pulls the image, writes logs and injects secrets at task start. It exists
# while keep_legacy_service is true; the split services have their own execution roles in roles.tf.
resource "aws_iam_role" "execution" {
  count = local.legacy_count

  name               = "${local.server_name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  count = local.legacy_count

  role       = aws_iam_role.execution[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "execution_secrets" {
  statement {
    sid     = "ReadServerSecrets"
    actions = ["secretsmanager:GetSecretValue"]
    resources = concat(
      [
        aws_secretsmanager_secret.database_url.arn,
        aws_secretsmanager_secret.session_key.arn,
      ],
      aws_secretsmanager_secret.metrics_token[*].arn,
      var.smtp_password_secret_arn == null ? [] : [var.smtp_password_secret_arn],
      local.kv_enabled ? [local.kv_url_secret_arn] : [],
    )
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
        # Region from the log group ARN, as ecs.tf does.
        values = ["secretsmanager.${split(":", aws_cloudwatch_log_group.server.arn)[3]}.amazonaws.com"]
      }
    }
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  count = local.legacy_count

  name   = "${local.server_name}-secrets"
  role   = aws_iam_role.execution[0].id
  policy = data.aws_iam_policy_document.execution_secrets.json
}

# Legacy task role (single service): what the Harbormaster process itself may do. Only sts:AssumeRole
# into the configured access roles (spec 11.3), plus an optional single-secret
# read for the bootstrap admin password. The split services have their own task roles in roles.tf.
resource "aws_iam_role" "task" {
  count = local.legacy_count

  name               = "${local.server_name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

data "aws_iam_policy_document" "task_assume" {
  statement {
    sid       = "AssumeAccessRoles"
    actions   = ["sts:AssumeRole"]
    resources = var.access_role_arns
  }
}

resource "aws_iam_role_policy" "task_assume" {
  count = local.legacy_count

  name   = "${local.server_name}-assume"
  role   = aws_iam_role.task[0].id
  policy = data.aws_iam_policy_document.task_assume.json
}

data "aws_iam_policy_document" "task_bootstrap" {
  count = var.bootstrap_admin_password_secret_arn == null ? 0 : 1

  statement {
    sid       = "ReadBootstrapAdminPassword"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.bootstrap_admin_password_secret_arn]
  }
}

resource "aws_iam_role_policy" "task_bootstrap" {
  count = var.bootstrap_admin_password_secret_arn == null ? 0 : local.legacy_count

  name   = "${local.server_name}-bootstrap"
  role   = aws_iam_role.task[0].id
  policy = data.aws_iam_policy_document.task_bootstrap[0].json
}

# The legacy resources gained a count so keep_legacy_service = false can remove them. Each moved block keeps an
# existing install's state: the resource is renamed to index 0, not replaced.
moved {
  from = aws_iam_role.execution
  to   = aws_iam_role.execution[0]
}

moved {
  from = aws_iam_role_policy_attachment.execution_managed
  to   = aws_iam_role_policy_attachment.execution_managed[0]
}

moved {
  from = aws_iam_role_policy.execution_secrets
  to   = aws_iam_role_policy.execution_secrets[0]
}

moved {
  from = aws_iam_role.task
  to   = aws_iam_role.task[0]
}

moved {
  from = aws_iam_role_policy.task_assume
  to   = aws_iam_role_policy.task_assume[0]
}
