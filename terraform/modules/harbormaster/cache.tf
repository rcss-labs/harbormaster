# The shared store for estate-wide limits, live page refresh and the snapshot cache (Valkey or Redis). Its data is
# disposable: when it is down or empty, Harbormaster falls back to Postgres and per-task behaviour. cache_mode = "create"
# builds it here; "external" takes a URL secret you manage; "none" uses no store.

resource "random_password" "cache_auth_token" {
  count = local.cache_create ? 1 : 0

  length  = 64
  special = false # ElastiCache rejects several punctuation characters, and the token goes into a URL
}

resource "aws_elasticache_subnet_group" "this" {
  count = local.cache_create ? 1 : 0

  name       = local.name
  subnet_ids = var.private_subnet_ids

  tags = local.tags
}

# volatile-lru: evict only keys that carry a TTL. Every key Harbormaster writes has one.
resource "aws_elasticache_parameter_group" "this" {
  count = local.cache_create ? 1 : 0

  name   = "${local.name}-${local.cache_family}"
  family = local.cache_family

  parameter {
    name  = "maxmemory-policy"
    value = "volatile-lru"
  }

  lifecycle {
    create_before_destroy = true
  }

  tags = local.tags
}

resource "aws_elasticache_replication_group" "this" {
  count = local.cache_create ? 1 : 0

  replication_group_id = local.name
  description          = "Harbormaster shared store (${var.cache_engine})"

  engine         = var.cache_engine
  engine_version = local.cache_engine_version
  node_type      = var.cache_node_type
  port           = 6379

  # Cluster mode off: one shard, 1 + replicas nodes.
  num_cache_clusters         = 1 + var.cache_replicas
  automatic_failover_enabled = var.cache_replicas > 0
  multi_az_enabled           = var.cache_replicas > 0

  subnet_group_name    = aws_elasticache_subnet_group.this[0].name
  parameter_group_name = aws_elasticache_parameter_group.this[0].name
  security_group_ids   = [aws_security_group.cache[0].id]

  transit_encryption_enabled = true
  at_rest_encryption_enabled = true
  kms_key_id                 = var.db_kms_key_id # null uses the AWS-owned key
  auth_token                 = random_password.cache_auth_token[0].result

  snapshot_retention_limit = 0 # the data is disposable
  apply_immediately        = false

  tags = local.tags
}

# The full URL, injected as HM_KV_URL. The token never appears in a task definition or an output.
resource "aws_secretsmanager_secret" "kv_url" {
  count = local.cache_create ? 1 : 0

  name       = "${local.name}/kv-url"
  kms_key_id = var.db_kms_key_id

  # The URL is rebuilt from the cache on every apply, so there is nothing to recover. Deleting at once lets a repeated
  # run 3 after a rollback re-create the name instead of failing on the 30-day recovery window.
  recovery_window_in_days = 0

  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "kv_url" {
  count = local.cache_create ? 1 : 0

  secret_id     = aws_secretsmanager_secret.kv_url[0].id
  secret_string = "rediss://:${random_password.cache_auth_token[0].result}@${aws_elasticache_replication_group.this[0].primary_endpoint_address}:6379/0"
}

# ElastiCache names the nodes of a replication group <id>-001, <id>-002 and so on, and publishes Evictions per node.
# Evictions mean the store is too small for what Harbormaster keeps in it: size the node up.
resource "aws_cloudwatch_metric_alarm" "cache_evictions" {
  count = local.cache_create ? 1 + var.cache_replicas : 0

  alarm_name          = "${local.name}-cache-evictions-${format("%03d", count.index + 1)}"
  alarm_description   = "The Harbormaster cache is evicting keys for 15 minutes. Size cache_node_type up."
  namespace           = "AWS/ElastiCache"
  metric_name         = "Evictions"
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 3
  datapoints_to_alarm = 3
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = var.cache_alarm_actions

  dimensions = {
    CacheClusterId = "${local.name}-${format("%03d", count.index + 1)}"
  }

  tags = local.tags

  depends_on = [aws_elasticache_replication_group.this]
}
