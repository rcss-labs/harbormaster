locals {
  name           = "harbormaster"
  server_name    = "harbormaster-server"
  container_port = 8480
  tags           = var.tags

  # Region and account from the log group ARN (arn:aws:logs:<region>:<account>:...): data.aws_region .name/.id are
  # deprecated in provider 6.x and .region is 6.x-only. The one place the region is computed.
  region     = split(":", aws_cloudwatch_log_group.server.arn)[3]
  account_id = split(":", aws_cloudwatch_log_group.server.arn)[4]

  cache_port    = 6379
  database_port = 5432

  # Autoscaling: average CPU target (percent) and cooldowns (seconds). Web and poller share the target and the
  # scale-out cooldown; scale-in is slower for web (it closes WebSockets) than for the poller.
  autoscaling_cpu_target         = 60
  autoscaling_scale_out_cooldown = 60
  web_scale_in_cooldown          = 600
  poller_scale_in_cooldown       = 300

  database_url = "postgres://harbormaster:${random_password.db.result}@${aws_db_instance.this.address}:${aws_db_instance.this.port}/harbormaster?sslmode=require"
}
