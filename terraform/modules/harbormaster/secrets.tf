resource "aws_secretsmanager_secret" "database_url" {
  name       = "${local.name}/database-url"
  kms_key_id = var.db_kms_key_id

  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "database_url" {
  secret_id     = aws_secretsmanager_secret.database_url.id
  secret_string = local.database_url
}

resource "random_id" "session_key" {
  byte_length = 32
}

resource "aws_secretsmanager_secret" "session_key" {
  name       = "${local.name}/session-key"
  kms_key_id = var.db_kms_key_id

  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "session_key" {
  secret_id     = aws_secretsmanager_secret.session_key.id
  secret_string = random_id.session_key.hex
}

resource "random_password" "metrics_token" {
  count = var.enable_metrics ? 1 : 0

  length  = 40
  special = false
}

resource "aws_secretsmanager_secret" "metrics_token" {
  count = var.enable_metrics ? 1 : 0

  name       = "${local.name}/metrics-token"
  kms_key_id = var.db_kms_key_id

  tags = local.tags
}

resource "aws_secretsmanager_secret_version" "metrics_token" {
  count = var.enable_metrics ? 1 : 0

  secret_id     = aws_secretsmanager_secret.metrics_token[0].id
  secret_string = random_password.metrics_token[0].result
}
