# -------------------------------------------------------------
# ROOT MODULE
# All child modules are wired together here.
# -------------------------------------------------------------

module "vpc" {
  source = "./modules/vpc"

  vpc_cidr     = var.vpc_cidr
  azs          = var.azs
  cluster_name = var.cluster_name
}

module "eks" {
  source = "./modules/eks"

  cluster_name     = var.cluster_name
  vpc_id           = module.vpc.vpc_id
  subnet_ids       = module.vpc.private_subnets
  instance_types    = var.node_instance_types
  desired_capacity = var.desired_capacity
  min_size         = var.min_size
  max_size         = var.max_size
}

module "redis" {
  source = "./modules/redis"

  subnet_ids      = module.vpc.private_subnets
  vpc_id          = module.vpc.vpc_id
  vpc_cidr        = var.vpc_cidr
  node_type       = var.redis_node_type
  num_cache_nodes = var.redis_replicas
}

module "ecr" {
  source = "./modules/ecr"

  repository_name = var.cluster_name
}
