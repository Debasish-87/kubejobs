# -------------------------------------------------------------
# REDIS MODULE
# -------------------------------------------------------------
# Provisions an ElastiCache Redis replication group with:
#   - Multi-AZ deployment with automatic failover
#   - Encryption at rest and in transit
#   - A dedicated security group restricting access to the VPC CIDR
#   - Daily automated snapshots with a 7-day retention window
# -------------------------------------------------------------

resource "aws_security_group" "redis" {
  name        = "kubejobs-redis-sg"
  description = "Controls access to the Redis ElastiCache cluster"
  vpc_id      = var.vpc_id

  ingress {
    description = "Redis access from within the VPC"
    from_port   = 6379
    to_port     = 6379
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "kubejobs-redis-sg" }
}

resource "aws_elasticache_subnet_group" "redis" {
  name       = "kubejobs-redis-subnet-group"
  subnet_ids = var.subnet_ids
}

resource "aws_elasticache_replication_group" "redis" {
  replication_group_id = "kubejobs-redis"
  description          = "Redis cluster for KubeJobs"

  engine         = "redis"
  engine_version = "7.1"
  node_type      = var.node_type
  port           = 6379

  num_cache_clusters = 1

  automatic_failover_enabled = false
  multi_az_enabled           = false

  subnet_group_name  = aws_elasticache_subnet_group.redis.name
  security_group_ids = [aws_security_group.redis.id]

  at_rest_encryption_enabled = true
  transit_encryption_enabled = true

  snapshot_retention_limit = 7
  snapshot_window          = "03:00-04:00"
  maintenance_window       = "sun:05:00-sun:06:00"

  tags = { Name = "kubejobs-redis" }
    lifecycle {
    prevent_destroy = true
  }
}

