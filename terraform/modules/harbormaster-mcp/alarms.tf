# Alarms exist only when alarm_sns_topic_arns is non-empty.
locals {
  alarms_enabled = length(var.alarm_sns_topic_arns) > 0

  alarms = local.alarms_enabled ? {
    alb-5xx = {
      description = "The public load balancer itself returned 5xx responses."
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_ELB_5XX_Count"
      statistic   = "Sum"
      comparison  = "GreaterThanOrEqualToThreshold"
      threshold   = 5
      dimensions  = { LoadBalancer = aws_lb.this.arn_suffix }
    }
    target-5xx = {
      description = "Gateway tasks returned 5xx responses."
      namespace   = "AWS/ApplicationELB"
      metric_name = "HTTPCode_Target_5XX_Count"
      statistic   = "Sum"
      comparison  = "GreaterThanOrEqualToThreshold"
      threshold   = 5
      dimensions  = { LoadBalancer = aws_lb.this.arn_suffix, TargetGroup = aws_lb_target_group.service.arn_suffix }
    }
    unhealthy-hosts = {
      description = "A gateway task fails its load balancer health check."
      namespace   = "AWS/ApplicationELB"
      metric_name = "UnHealthyHostCount"
      statistic   = "Maximum"
      comparison  = "GreaterThanOrEqualToThreshold"
      threshold   = 1
      dimensions  = { LoadBalancer = aws_lb.this.arn_suffix, TargetGroup = aws_lb_target_group.service.arn_suffix }
    }
    waf-blocked = {
      description = "WAF is blocking an unusual number of requests (attack, or a legitimate client being rate limited)."
      namespace   = "AWS/WAFV2"
      metric_name = "BlockedRequests"
      statistic   = "Sum"
      comparison  = "GreaterThanOrEqualToThreshold"
      threshold   = 100
      dimensions  = { WebACL = aws_wafv2_web_acl.this.name, Region = local.region, Rule = "ALL" }
    }
  } : {}
}

resource "aws_cloudwatch_metric_alarm" "this" {
  for_each = local.alarms

  alarm_name          = "${var.name}-${each.key}"
  alarm_description   = each.value.description
  namespace           = each.value.namespace
  metric_name         = each.value.metric_name
  statistic           = each.value.statistic
  comparison_operator = each.value.comparison
  threshold           = each.value.threshold
  dimensions          = each.value.dimensions
  period              = 300
  evaluation_periods  = 1
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.alarm_sns_topic_arns
  ok_actions          = var.alarm_sns_topic_arns

  tags = local.tags
}
