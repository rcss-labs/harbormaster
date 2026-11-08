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
      type        = "AWS"
      identifiers = [var.harbormaster_task_role_arn]
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
      "ecr:DescribeImages",
      "ecr:DescribeRepositories",
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
