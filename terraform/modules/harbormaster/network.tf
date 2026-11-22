resource "aws_security_group" "alb" {
  name        = "${local.name}-alb"
  description = "Harbormaster ALB"
  vpc_id      = var.vpc_id

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = var.allowed_cidrs
  }

  dynamic "ingress" {
    for_each = var.http_redirect ? [1] : []
    content {
      description = "HTTP (redirected to HTTPS)"
      from_port   = 80
      to_port     = 80
      protocol    = "tcp"
      cidr_blocks = var.allowed_cidrs
    }
  }

  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  lifecycle {
    # Terraform 1.5 variable validation cannot reference other variables.
    precondition {
      condition     = var.internal || length(var.public_subnet_ids) > 0
      error_message = "public_subnet_ids must be non-empty unless internal = true."
    }
  }

  tags = local.tags
}

resource "aws_security_group" "server" {
  name        = local.server_name
  description = "Harbormaster server tasks"
  vpc_id      = var.vpc_id

  ingress {
    description     = "Harbormaster port from the ALB only"
    from_port       = local.container_port
    to_port         = local.container_port
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  egress {
    description = "All outbound (AWS APIs, RDS, Secrets Manager)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

# The security groups of the split services. They are created whatever split_services says, like the roles, so the
# database group does not change when the services come and go. The legacy server group above stays too, even after the
# legacy service is removed, so removing it changes nothing about the database rule.
resource "aws_security_group" "web" {
  name        = "${local.name}-web"
  description = "Harbormaster web tasks"
  vpc_id      = var.vpc_id

  ingress {
    description     = "Harbormaster port from the ALB only"
    from_port       = local.container_port
    to_port         = local.container_port
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  egress {
    description = "All outbound (AWS APIs, RDS, the cache, Secrets Manager)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

# Poller and coordinator tasks take no traffic. The container port is open only to the groups that scrape /metrics.
resource "aws_security_group" "poller" {
  name        = "${local.name}-poller"
  description = "Harbormaster poller tasks"
  vpc_id      = var.vpc_id

  dynamic "ingress" {
    for_each = length(var.metrics_ingress_security_group_ids) > 0 ? [1] : []
    content {
      description     = "Metrics scrape"
      from_port       = local.container_port
      to_port         = local.container_port
      protocol        = "tcp"
      security_groups = var.metrics_ingress_security_group_ids
    }
  }

  egress {
    description = "All outbound (AWS APIs, RDS, the cache, Secrets Manager)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

resource "aws_security_group" "coordinator" {
  name        = "${local.name}-coordinator"
  description = "Harbormaster coordinator tasks"
  vpc_id      = var.vpc_id

  dynamic "ingress" {
    for_each = length(var.metrics_ingress_security_group_ids) > 0 ? [1] : []
    content {
      description     = "Metrics scrape"
      from_port       = local.container_port
      to_port         = local.container_port
      protocol        = "tcp"
      security_groups = var.metrics_ingress_security_group_ids
    }
  }

  egress {
    description = "All outbound (AWS APIs, RDS, the cache, Secrets Manager)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

resource "aws_security_group" "cache" {
  count = local.cache_create ? 1 : 0

  name        = "${local.name}-cache"
  description = "Harbormaster Valkey or Redis"
  vpc_id      = var.vpc_id

  ingress {
    description = "Store port from the Harbormaster tasks only"
    from_port   = 6379
    to_port     = 6379
    protocol    = "tcp"
    security_groups = concat(
      [
        aws_security_group.web.id,
        aws_security_group.poller.id,
        aws_security_group.coordinator.id,
      ],
      # The legacy task connects to the store while both run (migration step 3), and only then.
      var.keep_legacy_service ? [aws_security_group.server.id] : [],
    )
  }

  tags = local.tags
}

resource "aws_security_group" "db" {
  name        = "${local.name}-db"
  description = "Harbormaster PostgreSQL"
  vpc_id      = var.vpc_id

  ingress {
    description = "PostgreSQL from the Harbormaster tasks only"
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    security_groups = [
      aws_security_group.server.id,
      aws_security_group.web.id,
      aws_security_group.poller.id,
      aws_security_group.coordinator.id,
    ]
  }

  tags = local.tags
}
