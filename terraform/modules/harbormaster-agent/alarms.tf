# Alarms exist only when alarm_sns_topic_arns is non-empty.
locals {
  alarms_enabled = length(var.alarm_sns_topic_arns) > 0

  alarms = local.alarms_enabled ? {
    alb-5xx = {
      description = "The agent load balancer itself returned 5xx responses."
      metric_name = "HTTPCode_ELB_5XX_Count"
      statistic   = "Sum"
      threshold   = 5
      dimensions  = { LoadBalancer = aws_lb.this.arn_suffix }
    }
    target-5xx = {
      description = "Agent tasks returned 5xx responses."
      metric_name = "HTTPCode_Target_5XX_Count"
      statistic   = "Sum"
      threshold   = 5
      dimensions  = { LoadBalancer = aws_lb.this.arn_suffix, TargetGroup = aws_lb_target_group.service.arn_suffix }
    }
    unhealthy-hosts = {
      description = "An agent task fails its load balancer health check."
      metric_name = "UnHealthyHostCount"
      statistic   = "Maximum"
      threshold   = 1
      dimensions  = { LoadBalancer = aws_lb.this.arn_suffix, TargetGroup = aws_lb_target_group.service.arn_suffix }
    }
  } : {}
}

resource "aws_cloudwatch_metric_alarm" "this" {
  for_each = local.alarms

  alarm_name          = "${var.name}-${each.key}"
  alarm_description   = each.value.description
  namespace           = "AWS/ApplicationELB"
  metric_name         = each.value.metric_name
  statistic           = each.value.statistic
  comparison_operator = "GreaterThanOrEqualToThreshold"
  threshold           = each.value.threshold
  dimensions          = each.value.dimensions
  period              = 300
  evaluation_periods  = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns

  tags = local.tags
}
