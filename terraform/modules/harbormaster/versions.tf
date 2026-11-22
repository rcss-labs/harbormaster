terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      # 5.73.0 first accepts engine = "valkey" on aws_elasticache_replication_group; 5.82.0 fixes the perpetual diff
      # (a replacement) on at_rest_encryption_enabled for valkey, which this module sets. Install locks use 6.x.
      version = ">= 5.82, < 7.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.5"
    }
  }
}
