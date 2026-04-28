# -------------------------------------------------------------
# GLOBAL
# -------------------------------------------------------------

variable "region" {
  description = "AWS region where all resources will be provisioned"
  type        = string
  default     = "ap-south-1"
}

variable "environment" {
  description = "Deployment environment (prod, staging, dev)"
  type        = string
  default     = "prod"
}

# -------------------------------------------------------------
# VPC
# -------------------------------------------------------------

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

variable "azs" {
  description = "List of availability zones to deploy across"
  type        = list(string)
  default     = ["ap-south-1a", "ap-south-1b"]
}

# -------------------------------------------------------------
# EKS
# -------------------------------------------------------------

variable "cluster_name" {
  description = "Name of the EKS cluster"
  type        = string
  default     = "kubejobs"
}

variable "node_instance_types" {
  description = "List of EC2 instance types for worker nodes"
  type        = list(string)
  default     = ["m7i-flex.large", "c7i-flex.large"]
}

variable "desired_capacity" {
  description = "Desired number of worker nodes"
  type        = number
  default     = 2
}

variable "min_size" {
  description = "Minimum number of worker nodes"
  type        = number
  default     = 1
}

variable "max_size" {
  description = "Maximum number of worker nodes"
  type        = number
  default     = 5
}

# -------------------------------------------------------------
# REDIS
# -------------------------------------------------------------

variable "redis_node_type" {
  description = "ElastiCache node instance type"
  type        = string
  default     = "cache.t3.micro"
}

variable "redis_replicas" {
  description = "Number of Redis nodes. Minimum 2 required for automatic failover"
  type        = number
  default     = 1
}
