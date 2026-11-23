resource "aws_lb" "this" {
  name                       = local.name
  internal                   = var.internal
  load_balancer_type         = "application"
  subnets                    = var.internal ? var.private_subnet_ids : var.public_subnet_ids
  security_groups            = [aws_security_group.alb.id]
  drop_invalid_header_fields = true
  idle_timeout               = var.alb_idle_timeout

  tags = local.tags

  lifecycle {
    # Access tokens are meant to reach Harbormaster only through the MCP gateway (WAF, bad-token limiter). With a
    # public load balancer a bearer token would be accepted straight from the internet, bypassing both.
    precondition {
      condition     = var.internal || lower(lookup(var.extra_env, "HM_API_TOKENS", "false")) != "true"
      error_message = "HM_API_TOKENS = \"true\" needs internal = true: tokens must reach Harbormaster only through the harbormaster-mcp gateway, not a public load balancer."
    }
  }
}

resource "aws_lb_target_group" "server" {
  name                 = local.server_name
  port                 = local.container_port
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = var.vpc_id
  deregistration_delay = 30

  health_check {
    path                = "/healthz"
    matcher             = "200"
    interval            = 15
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = local.tags
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  certificate_arn   = var.certificate_arn
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.server.arn
  }

  tags = local.tags
}

resource "aws_lb_listener" "http" {
  count = var.http_redirect ? 1 : 0

  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"

    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }

  tags = local.tags
}
