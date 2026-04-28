variable "subnet_ids" {
  description = "List of private subnet IDs for the ElastiCache subnet group"
  type        = list(string)
}

variable "vpc_id" {
  description = "VPC ID used to create the Redis security group"
  type        = string
}

variable "vpc_cidr" {
  description = "VPC CIDR block used to restrict Redis ingress to internal traffic only"
  type        = string
}

variable "node_type" {
  description = "ElastiCache node instance type"
  type        = string
  default     = "cache.t3.micro"
}

variable "num_cache_nodes" {
  description = "Number of cache nodes. Must be at least 2 when automatic_failover_enabled is true"
  type        = number
  default     = 2
}
