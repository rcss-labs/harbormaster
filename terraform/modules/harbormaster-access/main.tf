data "aws_caller_identity" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id

  # Spec 11.2 example patterns, used unless the caller supplies pass_role_arns.
  pass_role_arns = var.pass_role_arns != null ? var.pass_role_arns : [
    "arn:aws:iam::${local.account_id}:role/*-task",
    "arn:aws:iam::${local.account_id}:role/ecs-task-execution",
  ]

  # Null resolves to every repository in this account, like pass_role_arns.
  scan_repository_arns = var.scan_repository_arns != null ? var.scan_repository_arns : [
    "arn:aws:ecr:*:${local.account_id}:repository/*",
  ]
}

data "aws_iam_policy_document" "trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type = "AWS"
      # The legacy single task role (while it exists) and the web task role: both serve the API and the terminals.
      identifiers = compact([var.harbormaster_task_role_arn, var.web_task_role_arn])
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.external_id]
    }
  }
}

data "aws_iam_policy_document" "read" {
  statement {
    sid    = "ReadOnlyInspection"
    effect = "Allow"
    actions = [
      "ecs:List*",
      "ecs:Describe*",
      "ecs:ListTagsForResource",
      "ec2:DescribeVpcs",
      "ec2:DescribeSubnets",
      "ec2:DescribeSecurityGroups",
      "ec2:DescribeNatGateways",
      "ec2:DescribeInternetGateways",
      "ec2:DescribeVpcEndpoints",
      "ec2:DescribeNetworkInterfaces",
      "elasticfilesystem:DescribeFileSystems",
      "elasticfilesystem:DescribeAccessPoints",
      "elasticfilesystem:DescribeMountTargets",
      "elasticloadbalancing:DescribeTargetGroups",
      "elasticloadbalancing:DescribeTargetHealth",
      "application-autoscaling:Describe*",
      "cloudwatch:GetMetricData",
      "logs:FilterLogEvents",
      "logs:GetLogEvents",
      "logs:StartLiveTail",
      "logs:StopLiveTail",
      "logs:StartQuery",
      "logs:GetQueryResults",
      "logs:StopQuery",
      "logs:DescribeLogGroups",
      "ecr:DescribeImages",
      "ecr:DescribeRepositories",
      "ecr:BatchGetImage",
    ]
    resources = ["*"]
  }
}

data "aws_iam_policy_document" "write" {
  statement {
    sid    = "EcsOperations"
    effect = "Allow"
    actions = [
      "ecs:RegisterTaskDefinition",
      "ecs:TagResource",
      "ecs:UpdateService",
      "ecs:StopTask",
      "ecs:ExecuteCommand",
      "ssm:TerminateSession",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "PassTaskRolesToEcs"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = local.pass_role_arns

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }

  statement {
    sid    = "AutoScalingWrites"
    effect = "Allow"
    actions = [
      "application-autoscaling:RegisterScalableTarget",
      "application-autoscaling:DeregisterScalableTarget",
      "application-autoscaling:PutScalingPolicy",
      "application-autoscaling:DeleteScalingPolicy",
      "application-autoscaling:PutScheduledAction",
      "application-autoscaling:DeleteScheduledAction",
    ]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "application-autoscaling:service-namespace"
      values   = ["ecs"]
    }
  }

  statement {
    sid    = "TargetTrackingAlarms"
    effect = "Allow"
    actions = [
      "cloudwatch:PutMetricAlarm",
      "cloudwatch:DeleteAlarms",
    ]
    resources = ["arn:aws:cloudwatch:*:${local.account_id}:alarm:TargetTracking-service/*"]
  }

  statement {
    sid       = "TargetTrackingAlarmsDescribe"
    effect    = "Allow"
    actions   = ["cloudwatch:DescribeAlarms"]
    resources = ["*"]
  }

  statement {
    sid       = "AutoScalingServiceLinkedRole"
    effect    = "Allow"
    actions   = ["iam:CreateServiceLinkedRole"]
    resources = ["arn:aws:iam::${local.account_id}:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService"]

    condition {
      test     = "StringEquals"
      variable = "iam:AWSServiceName"
      values   = ["ecs.application-autoscaling.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "this" {
  lifecycle {
    precondition {
      condition     = var.harbormaster_task_role_arn != null || var.web_task_role_arn != null
      error_message = "Set harbormaster_task_role_arn (single service) or web_task_role_arn (split services): the access role must trust at least one task role."
    }
  }

  name                 = var.role_name
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
  tags                 = var.tags
}

resource "aws_iam_role_policy" "read" {
  name   = "harbormaster-access-read"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.read.json
}

resource "aws_iam_role_policy" "write" {
  count = var.read_only ? 0 : 1

  name   = "harbormaster-access-write"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.write.json
}

# ---- Least-privilege roles for the poller and the coordinator ------------------------------------------------------
# With split services the poller assumes <role_name>-poller and the coordinator <role_name>-coordinator (Harbormaster
# appends the suffix to the stored role ARN). Each action list is tied to the AWS calls the code makes by the Go test
# TestAccessPolicies_CoverEachRolesCallsAndNothingBroader, which reads the lines between the "policy" markers below.

data "aws_iam_policy_document" "poller_trust" {
  count = var.poller_task_role_arn == null ? 0 : 1

  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = [var.poller_task_role_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.external_id]
    }
  }
}

data "aws_iam_policy_document" "poller" {
  statement {
    sid    = "PollerReadOnly"
    effect = "Allow"
    # policy: poller
    actions = [
      "ecs:List*",
      "ecs:Describe*",
      "ecs:ListTagsForResource",
      "ec2:DescribeVpcs",
      "ec2:DescribeSubnets",
      "ec2:DescribeSecurityGroups",
      "ec2:DescribeNatGateways",
      "ec2:DescribeInternetGateways",
      "ec2:DescribeVpcEndpoints",
      "ec2:DescribeNetworkInterfaces",
      "elasticfilesystem:DescribeFileSystems",
      "elasticfilesystem:DescribeAccessPoints",
      "elasticfilesystem:DescribeMountTargets",
      "elasticloadbalancing:DescribeTargetGroups",
      "elasticloadbalancing:DescribeTargetHealth",
      "application-autoscaling:Describe*",
    ]
    # end policy
    resources = ["*"]
  }
}

resource "aws_iam_role" "poller" {
  count = var.poller_task_role_arn == null ? 0 : 1

  name                 = "${var.role_name}-poller"
  assume_role_policy   = data.aws_iam_policy_document.poller_trust[0].json
  max_session_duration = 3600
  tags                 = var.tags
}

resource "aws_iam_role_policy" "poller" {
  count = var.poller_task_role_arn == null ? 0 : 1

  name   = "harbormaster-access-poller"
  role   = aws_iam_role.poller[0].id
  policy = data.aws_iam_policy_document.poller.json
}

data "aws_iam_policy_document" "coordinator_trust" {
  count = var.coordinator_task_role_arn == null ? 0 : 1

  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = [var.coordinator_task_role_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.external_id]
    }
  }
}

data "aws_iam_policy_document" "coordinator" {
  # policy: coordinator
  statement {
    sid    = "CoordinatorRead"
    effect = "Allow"
    actions = [
      "ecs:DescribeServices",
      "application-autoscaling:Describe*",
      "ecr:BatchGetImage",
      "ecr:DescribeImages",
      "cloudwatch:GetMetricData",
    ]
    resources = ["*"]
  }

  # Off-hours puts a service to sleep and wakes it. It registers the scalable target's range (its own target when the
  # service has none), keeps its scheduled actions and restores the desired count when a service leaves. read_only
  # drops these, so off-hours cannot act, as with the single role.
  dynamic "statement" {
    for_each = var.read_only ? [] : [1]
    content {
      sid    = "OffHoursWrites"
      effect = "Allow"
      actions = [
        "ecs:UpdateService",
        "application-autoscaling:RegisterScalableTarget",
        "application-autoscaling:DeregisterScalableTarget",
        "application-autoscaling:PutScheduledAction",
        "application-autoscaling:DeleteScheduledAction",
      ]
      resources = ["*"]

      condition {
        test     = "StringEquals"
        variable = "application-autoscaling:service-namespace"
        values   = ["ecs"]
      }
    }
  }

  # The first RegisterScalableTarget in an account that never used ECS autoscaling creates this service-linked role
  # and fails without the permission. The single role has the same statement.
  dynamic "statement" {
    for_each = var.read_only ? [] : [1]
    content {
      sid       = "AutoScalingServiceLinkedRole"
      effect    = "Allow"
      actions   = ["iam:CreateServiceLinkedRole"]
      resources = ["arn:aws:iam::${local.account_id}:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_ECSService"]

      condition {
        test     = "StringEquals"
        variable = "iam:AWSServiceName"
        values   = ["ecs.application-autoscaling.amazonaws.com"]
      }
    }
  }
  # end policy
}

resource "aws_iam_role" "coordinator" {
  count = var.coordinator_task_role_arn == null ? 0 : 1

  name                 = "${var.role_name}-coordinator"
  assume_role_policy   = data.aws_iam_policy_document.coordinator_trust[0].json
  max_session_duration = 3600
  tags                 = var.tags
}

resource "aws_iam_role_policy" "coordinator" {
  count = var.coordinator_task_role_arn == null ? 0 : 1

  name   = "harbormaster-access-coordinator"
  role   = aws_iam_role.coordinator[0].id
  policy = data.aws_iam_policy_document.coordinator.json
}

# The scan role is trusted by the scanner task only, never by the server, and holds read-only ECR pull
# actions, so it is created regardless of read_only.
data "aws_iam_policy_document" "scan_trust" {
  count = var.scanner_task_role_arn == null ? 0 : 1

  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "AWS"
      identifiers = [var.scanner_task_role_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "sts:ExternalId"
      values   = [var.external_id]
    }
  }
}

data "aws_iam_policy_document" "scan_pull" {
  count = var.scanner_task_role_arn == null ? 0 : 1

  statement {
    sid       = "EcrAuthToken"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid    = "EcrPull"
    effect = "Allow"
    actions = [
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
      "ecr:DescribeImages",
    ]
    resources = local.scan_repository_arns
  }
}

resource "aws_iam_role" "scan" {
  count = var.scanner_task_role_arn == null ? 0 : 1

  name                 = var.scan_role_name
  assume_role_policy   = data.aws_iam_policy_document.scan_trust[0].json
  max_session_duration = 3600
  tags                 = var.tags
}

resource "aws_iam_role_policy" "scan_pull" {
  count = var.scanner_task_role_arn == null ? 0 : 1

  name   = "harbormaster-scan-pull"
  role   = aws_iam_role.scan[0].id
  policy = data.aws_iam_policy_document.scan_pull[0].json
}
