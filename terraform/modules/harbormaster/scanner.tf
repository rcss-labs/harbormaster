# Image vulnerability scanning. Everything here exists only when enable_image_scanning is true.
#
# Two principals touch the bucket and nothing else does:
#   - the server task role (aws_iam_role_policy.task_scan) starts, watches and stops scanner tasks, writes jobs and
#     the vulnerability database, and reads and deletes results;
#   - the scanner task role reads jobs and the database, writes results, and assumes the per-account scan role.
# The scanner has its own execution role. aws_iam_role.execution can read the database URL and the session key, so
# the scanner never uses it.

locals {
  scan_enabled = var.enable_image_scanning

  # Region and account from the log group ARN (arn:aws:logs:<region>:<account>:...), as ecs.tf does for the region:
  # data.aws_region .name/.id are deprecated in provider 6.x and .region is 6.x-only.
  region     = split(":", aws_cloudwatch_log_group.server.arn)[3]
  account_id = split(":", aws_cloudwatch_log_group.server.arn)[4]

  scan_bucket_name = coalesce(var.scan_bucket_name, "harbormaster-scan-${local.account_id}-${local.region}")
  scan_role_arns   = coalesce(var.scan_role_arns, ["arn:aws:iam::*:role/${var.scan_role_name}"])

  scanner_name          = "harbormaster-scanner"
  scanner_container     = "scanner" # must equal awsx.ScannerContainerName: the server overrides this container's environment
  scanner_image_ref     = var.scanner_image == null ? "" : var.scanner_image
  scanner_image_digest  = try(split("@", local.scanner_image_ref)[1], "")
  ecs_task_arn_prefix   = "arn:aws:ecs:${local.region}:${local.account_id}:task/${aws_ecs_cluster.this.name}"
  scanner_task_def_arns = "arn:aws:ecs:${local.region}:${local.account_id}:task-definition/${local.scanner_name}:*"

  # Merged into container_env; the keys are rejected in extra_env.
  scan_server_env = local.scan_enabled ? {
    HM_SCAN_ENABLED         = "true"
    HM_SCAN_CLUSTER_ARN     = aws_ecs_cluster.this.arn
    HM_SCAN_TASK_DEFINITION = aws_ecs_task_definition.scanner[0].arn # includes the revision
    HM_SCAN_SUBNETS         = join(",", var.private_subnet_ids)
    HM_SCAN_SECURITY_GROUP  = aws_security_group.scanner[0].id
    HM_SCAN_BUCKET          = aws_s3_bucket.scan[0].bucket
    HM_SCAN_ROLE_NAME       = var.scan_role_name
  } : {}
}

# ---- Bucket ------------------------------------------------------------------------------------------------------

resource "aws_s3_bucket" "scan" {
  count = local.scan_enabled ? 1 : 0

  bucket = local.scan_bucket_name

  tags = local.tags
}

resource "aws_s3_bucket_public_access_block" "scan" {
  count = local.scan_enabled ? 1 : 0

  bucket                  = aws_s3_bucket.scan[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# SSE-S3 only. The database KMS key (var.db_kms_key_id) is not used: it would need kms grants on both roles and would
# share the database's key with the scan data.
resource "aws_s3_bucket_server_side_encryption_configuration" "scan" {
  count = local.scan_enabled ? 1 : 0

  bucket = aws_s3_bucket.scan[0].id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# grype-db/ must outlive the server's HM_SCAN_DB_MAX_AGE (120 h by default); the server also deletes jobs/ and
# results/ a day after a run ends, so these expiries are the backstop.
resource "aws_s3_bucket_lifecycle_configuration" "scan" {
  count = local.scan_enabled ? 1 : 0

  bucket = aws_s3_bucket.scan[0].id

  rule {
    id     = "expire-jobs"
    status = "Enabled"
    filter {
      prefix = "jobs/"
    }
    expiration {
      days = 7
    }
  }

  rule {
    id     = "expire-results"
    status = "Enabled"
    filter {
      prefix = "results/"
    }
    expiration {
      days = 30
    }
  }

  rule {
    id     = "expire-grype-db"
    status = "Enabled"
    filter {
      prefix = "grype-db/"
    }
    expiration {
      days = 10
    }
  }

  rule {
    id     = "abort-incomplete-multipart-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}

# ---- Network and logs --------------------------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "scanner" {
  count = local.scan_enabled ? 1 : 0

  name              = "/ecs/${local.name}/${local.scanner_name}"
  retention_in_days = var.log_retention_days

  tags = local.tags
}

# HTTPS only (ECR, S3, STS). The scanner never contacts the vulnerability data publisher: the server stores the
# database in the bucket. No ingress, and the database security group keeps admitting the server group only.
resource "aws_security_group" "scanner" {
  count = local.scan_enabled ? 1 : 0

  name        = local.scanner_name
  description = "Harbormaster scanner tasks"
  vpc_id      = var.vpc_id

  egress {
    description = "HTTPS to ECR, S3 and STS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = var.scanner_egress_cidrs
  }

  tags = local.tags
}

# ---- Scanner roles -----------------------------------------------------------------------------------------------

# Execution role: image pull and logs only. Never aws_iam_role.execution, which can read the database URL and the
# session key.
resource "aws_iam_role" "scanner_execution" {
  count = local.scan_enabled ? 1 : 0

  name               = "${local.scanner_name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

resource "aws_iam_role_policy_attachment" "scanner_execution_managed" {
  count = local.scan_enabled ? 1 : 0

  role       = aws_iam_role.scanner_execution[0].name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role" "scanner_task" {
  count = local.scan_enabled ? 1 : 0

  name               = "${local.scanner_name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json

  tags = local.tags
}

# No s3:ListBucket: the scanner treats a missing and a forbidden object alike.
data "aws_iam_policy_document" "scanner_task" {
  count = local.scan_enabled ? 1 : 0

  statement {
    sid       = "AssumeScanRoles"
    actions   = ["sts:AssumeRole"]
    resources = local.scan_role_arns
  }

  statement {
    sid     = "ReadJobsAndVulnerabilityData"
    actions = ["s3:GetObject"]
    resources = [
      "${aws_s3_bucket.scan[0].arn}/jobs/*",
      "${aws_s3_bucket.scan[0].arn}/grype-db/*",
    ]
  }

  statement {
    sid       = "WriteResults"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.scan[0].arn}/results/*"]
  }
}

resource "aws_iam_role_policy" "scanner_task" {
  count = local.scan_enabled ? 1 : 0

  name   = "${local.scanner_name}-task"
  role   = aws_iam_role.scanner_task[0].id
  policy = data.aws_iam_policy_document.scanner_task[0].json
}

# ---- Server role: scan permissions -------------------------------------------------------------------------------

data "aws_iam_policy_document" "task_scan" {
  count = local.scan_enabled ? 1 : 0

  statement {
    sid       = "RunScannerTask"
    actions   = ["ecs:RunTask"]
    resources = [local.scanner_task_def_arns]

    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [aws_ecs_cluster.this.arn]
    }
  }

  # RunTask fails without this because the launch tags the task.
  statement {
    sid       = "TagScannerTaskAtLaunch"
    actions   = ["ecs:TagResource"]
    resources = ["${local.ecs_task_arn_prefix}/*"]

    condition {
      test     = "StringEquals"
      variable = "ecs:CreateAction"
      values   = ["RunTask"]
    }
  }

  statement {
    sid       = "WatchAndStopTasks"
    actions   = ["ecs:DescribeTasks", "ecs:StopTask"]
    resources = ["${local.ecs_task_arn_prefix}/*"]

    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [aws_ecs_cluster.this.arn]
    }
  }

  statement {
    sid     = "PassScannerRoles"
    actions = ["iam:PassRole"]
    resources = [
      aws_iam_role.scanner_task[0].arn,
      aws_iam_role.scanner_execution[0].arn,
    ]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }

  # s3:PutObject also covers CreateMultipartUpload, UploadPart and CompleteMultipartUpload.
  statement {
    sid     = "WriteJobsAndVulnerabilityData"
    actions = ["s3:PutObject"]
    resources = [
      "${aws_s3_bucket.scan[0].arn}/jobs/*",
      "${aws_s3_bucket.scan[0].arn}/grype-db/*",
    ]
  }

  statement {
    sid       = "AbortArchiveUploads"
    actions   = ["s3:AbortMultipartUpload"]
    resources = ["${aws_s3_bucket.scan[0].arn}/grype-db/*"]
  }

  # The server never reads the archive, only the listing that names it.
  statement {
    sid     = "ReadResultsAndListing"
    actions = ["s3:GetObject"]
    resources = [
      "${aws_s3_bucket.scan[0].arn}/results/*",
      "${aws_s3_bucket.scan[0].arn}/grype-db/v6/latest.json",
    ]
  }

  statement {
    sid     = "DeleteFinishedRunObjects"
    actions = ["s3:DeleteObject"]
    resources = [
      "${aws_s3_bucket.scan[0].arn}/jobs/*",
      "${aws_s3_bucket.scan[0].arn}/results/*",
    ]
  }

  # Only so that a missing object answers 404 (ErrObjectNotFound) and not 403. S3 evaluates this permission on the
  # bucket for a GetObject on a missing key, and a StringLike condition on s3:prefix would not match there because
  # the key is absent from a GetObject request, so there is deliberately no prefix condition.
  statement {
    sid       = "ListBucketForNotFound"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.scan[0].arn]
  }
}

resource "aws_iam_role_policy" "task_scan" {
  count = local.scan_enabled ? 1 : 0

  name   = "${local.server_name}-scan"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_scan[0].json
}

# ---- Scanner task definition -------------------------------------------------------------------------------------

resource "aws_ecs_task_definition" "scanner" {
  count = local.scan_enabled ? 1 : 0

  family                   = local.scanner_name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.scanner_cpu
  memory                   = var.scanner_memory
  execution_role_arn       = aws_iam_role.scanner_execution[0].arn
  task_role_arn            = aws_iam_role.scanner_task[0].arn

  ephemeral_storage {
    size_in_gib = var.scanner_ephemeral_storage_gib
  }

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  volume {
    name = "work"
  }

  # No secrets block: the scanner gets the bucket and its own digest from here, and the run id and mode from the
  # server's RunTask overrides. A task that runs to completion has no healthCheck.
  container_definitions = jsonencode([
    {
      name      = local.scanner_container
      image     = local.scanner_image_ref
      essential = true

      environment = [
        { name = "HM_SCAN_BUCKET", value = aws_s3_bucket.scan[0].bucket },
        { name = "HM_SCAN_IMAGE_DIGEST", value = local.scanner_image_digest },
      ]

      # /work must exist in the image owned by uid 65532 (Dockerfile.scanner); the Fargate volume copies that ownership.
      mountPoints = [
        { sourceVolume = "work", containerPath = "/work", readOnly = false }
      ]

      readonlyRootFilesystem = true

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.scanner[0].name
          "awslogs-region"        = local.region
          "awslogs-stream-prefix" = "ecs"
        }
      }
    }
  ])

  tags = local.tags

  lifecycle {
    # Terraform 1.5 variable validation cannot reference other variables.
    precondition {
      condition     = var.scanner_image != null
      error_message = "scanner_image must be set (pinned by @sha256 digest) when enable_image_scanning is true."
    }
  }
}
