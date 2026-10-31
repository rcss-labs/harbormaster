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

resource "aws_security_group" "db" {
  name        = "${local.name}-db"
  description = "Harbormaster PostgreSQL"
  vpc_id      = var.vpc_id

  ingress {
    description     = "PostgreSQL from the server only"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [aws_security_group.server.id]
  }

  tags = local.tags
}
