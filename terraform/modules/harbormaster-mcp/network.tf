resource "aws_security_group" "alb" {
  name        = "${var.name}-alb"
  description = "Harbormaster MCP gateway public ALB"
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

  # Open egress: limiting it to the task group inline would make the two groups reference each other.
  egress {
    description = "All outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

# The task group has no inline rules. The existing module's load balancer group references this group's id, and this
# module references that group's id; separate rule resources keep the two modules free of a dependency cycle and keep
# Terraform from removing rules managed elsewhere.
resource "aws_security_group" "task" {
  name        = "${var.name}-task"
  description = "Harbormaster MCP gateway tasks"
  vpc_id      = var.vpc_id

  tags = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "task_from_alb" {
  security_group_id            = aws_security_group.task.id
  referenced_security_group_id = aws_security_group.alb.id
  description                  = "Gateway port from the public load balancer only"
  ip_protocol                  = "tcp"
  from_port                    = local.container_port
  to_port                      = local.container_port

  tags = local.tags
}

resource "aws_vpc_security_group_egress_rule" "task_to_harbormaster" {
  security_group_id            = aws_security_group.task.id
  referenced_security_group_id = var.harbormaster_alb_security_group_id
  description                  = "Harbormaster internal load balancer"
  ip_protocol                  = "tcp"
  from_port                    = 443
  to_port                      = 443

  tags = local.tags
}

# Covers the rule above. Kept separate so tightening later (VPC endpoints) is deleting this one rule.
resource "aws_vpc_security_group_egress_rule" "task_to_internet" {
  security_group_id = aws_security_group.task.id
  cidr_ipv4         = "0.0.0.0/0"
  description       = "AWS APIs through NAT: image pull and logs"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443

  tags = local.tags
}
