locals {
  name           = "harbormaster"
  server_name    = "harbormaster-server"
  container_port = 8480
  tags           = var.tags

  database_url = "postgres://harbormaster:${random_password.db.result}@${aws_db_instance.this.address}:${aws_db_instance.this.port}/harbormaster?sslmode=require"
}
