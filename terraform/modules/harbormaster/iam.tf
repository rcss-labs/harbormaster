data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Execution role: pulls the image, writes logs and injects secrets at task start.
resource "aws_iam_role" "execution" {
  name               = "${local.server_name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "execution_managed" {
  role       = aws_iam_role.execution.name
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
    )
  }

  dynamic "statement" {
    for_each = var.db_kms_key_id == null ? [] : [var.db_kms_key_id]
    content {
      sid       = "DecryptSecrets"
      actions   = ["kms:Decrypt"]
      resources = [statement.value]
    }
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  name   = "${local.server_name}-secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution_secrets.json
}

# Task role: what the Harbormaster process itself may do. Only sts:AssumeRole
# into the configured access roles (spec 11.3), plus an optional single-secret
# read for the bootstrap admin password.
resource "aws_iam_role" "task" {
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
  name   = "${local.server_name}-assume"
  role   = aws_iam_role.task.id
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
  count = var.bootstrap_admin_password_secret_arn == null ? 0 : 1

  name   = "${local.server_name}-bootstrap"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_bootstrap[0].json
}
