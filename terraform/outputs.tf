# -------------------------------------------------------------
# OUTPUTS
# Printed to the terminal after terraform apply completes.
# -------------------------------------------------------------

output "vpc_id" {
  description = "ID of the VPC"
  value       = module.vpc.vpc_id
}

output "public_subnets" {
  description = "IDs of public subnets"
  value       = module.vpc.public_subnets
}

output "private_subnets" {
  description = "IDs of private subnets"
  value       = module.vpc.private_subnets
}

output "cluster_name" {
  description = "EKS cluster name"
  value       = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "EKS API server endpoint"
  value       = module.eks.cluster_endpoint
}

output "configure_kubectl" {
  description = "Run this command to configure kubectl after the cluster is ready"
  value       = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name}"
}

output "redis_endpoint" {
  description = "Redis primary endpoint address. Use this as the REDIS_URL in your application"
  value       = module.redis.redis_endpoint
}

output "ecr_repository_url" {
  description = "ECR repository URL for docker push"
  value       = module.ecr.repository_url
}

output "ecr_push_commands" {
  description = "Commands to authenticate and push a Docker image to ECR"
  value       = <<-EOT
    aws ecr get-login-password --region ${var.region} | docker login --username AWS --password-stdin ${module.ecr.repository_url}
    docker build -t ${var.cluster_name} .
    docker tag ${var.cluster_name}:latest ${module.ecr.repository_url}:latest
    docker push ${module.ecr.repository_url}:latest
  EOT
}

output "redis_url" {
  description = "Full Redis connection string"
  value       = "${module.redis.redis_endpoint}:${module.redis.redis_port}"
}

output "cluster_region" {
  description = "AWS region of the cluster"
  value       = var.region
}
