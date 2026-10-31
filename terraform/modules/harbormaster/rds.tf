resource "random_password" "db" {
  length  = 32
  special = false # keeps the DSN URL-safe
}

resource "aws_db_subnet_group" "this" {
  name       = local.name
  subnet_ids = var.private_subnet_ids

  tags = local.tags
}

resource "aws_db_instance" "this" {
  identifier = local.name

  engine         = "postgres"
  engine_version = var.db_engine_version
  instance_class = var.db_instance_class

  storage_type          = "gp3"
  allocated_storage     = var.db_allocated_storage
  max_allocated_storage = var.db_max_allocated_storage
  storage_encrypted     = true
  kms_key_id            = var.db_kms_key_id

  db_name  = local.name
  username = local.name
  password = random_password.db.result

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.db.id]
  publicly_accessible    = false
  multi_az               = var.db_multi_az

  backup_retention_period    = 7
  deletion_protection        = var.db_deletion_protection
  skip_final_snapshot        = false
  final_snapshot_identifier  = "${local.name}-final"
  apply_immediately          = false
  auto_minor_version_upgrade = true
  copy_tags_to_snapshot      = true

  tags = local.tags
}
