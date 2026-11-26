# Neither group has inline rules. The harbormaster module's load balancer group lists this module's task group (through
# alb_ingress_security_group_ids), and this module references that module's groups; separate rule resources keep the two
# modules free of a dependency cycle and keep Terraform from removing rules managed elsewhere.
resource "aws_security_group" "alb" {
  name        = "${var.name}-alb"
  description = "Harbormaster agent internal ALB"
  vpc_id      = var.vpc_id

  tags = local.tags
}

# The only callers: Harbormaster's own tasks.
resource "aws_vpc_security_group_ingress_rule" "alb_from_harbormaster" {
  count = length(var.harbormaster_task_security_group_ids)

  security_group_id            = aws_security_group.alb.id
  referenced_security_group_id = var.harbormaster_task_security_group_ids[count.index]
  description                  = "HTTPS from a Harbormaster task group"
  ip_protocol                  = "tcp"
  from_port                    = 443
  to_port                      = 443

  tags = local.tags
}

resource "aws_vpc_security_group_egress_rule" "alb_to_task" {
  security_group_id            = aws_security_group.alb.id
  referenced_security_group_id = aws_security_group.task.id
  description                  = "Agent port to the tasks"
  ip_protocol                  = "tcp"
  from_port                    = local.container_port
  to_port                      = local.container_port

  tags = local.tags
}

resource "aws_security_group" "task" {
  name        = "${var.name}-task"
  description = "Harbormaster agent tasks"
  vpc_id      = var.vpc_id

  tags = local.tags
}

resource "aws_vpc_security_group_ingress_rule" "task_from_alb" {
  security_group_id            = aws_security_group.task.id
  referenced_security_group_id = aws_security_group.alb.id
  description                  = "Agent port from the internal load balancer only"
  ip_protocol                  = "tcp"
  from_port                    = local.container_port
  to_port                      = local.container_port

  tags = local.tags
}

# Tool calls: the agent reads Harbormaster through its internal load balancer.
resource "aws_vpc_security_group_egress_rule" "task_to_harbormaster" {
  security_group_id            = aws_security_group.task.id
  referenced_security_group_id = var.harbormaster_alb_security_group_id
  description                  = "Harbormaster internal load balancer"
  ip_protocol                  = "tcp"
  from_port                    = 443
  to_port                      = 443

  tags = local.tags
}

# Model providers, image pull and logs. Security groups cannot filter by hostname, so this is open on 443; the fence is
# the agent's code (it dials only the configured providers and Harbormaster). See the README for stricter options.
resource "aws_vpc_security_group_egress_rule" "task_to_internet" {
  security_group_id = aws_security_group.task.id
  cidr_ipv4         = "0.0.0.0/0"
  description       = "Model providers, image pull and logs through NAT"
  ip_protocol       = "tcp"
  from_port         = 443
  to_port           = 443

  tags = local.tags
}
