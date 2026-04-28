# -------------------------------------------------------------
# EKS MODULE
# -------------------------------------------------------------
# Uses the official terraform-aws-modules/eks module.
# The module manages all cluster and node group IAM roles internally.
#
# Cluster addons installed:
#   - CoreDNS        : in-cluster DNS resolution
#   - kube-proxy     : network proxy on each node
#   - vpc-cni        : pod networking (assigns VPC IPs to pods)
#   - aws-ebs-csi-driver : persistent volume support (PVC)
# -------------------------------------------------------------

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"

  cluster_name    = var.cluster_name
  cluster_version = "1.28"

  cluster_endpoint_public_access  = true
  cluster_endpoint_private_access = true

  vpc_id     = var.vpc_id
  subnet_ids = var.subnet_ids

  cluster_addons = {
    coredns = {
      most_recent = true
    }
    kube-proxy = {
      most_recent = true
    }
    vpc-cni = {
      most_recent = true
    }
    aws-ebs-csi-driver = {
      most_recent              = true
      service_account_role_arn = module.ebs_csi_irsa_role.iam_role_arn
    }
  }

  eks_managed_node_groups = {
    default = {
      desired_size   = var.desired_capacity
      max_size       = var.max_size
      min_size       = var.min_size

      instance_types = var.instance_types
      capacity_type  = "SPOT"

      ami_type = "AL2_x86_64"

      disk_size = 30

      iam_role_additional_policies = {
        AmazonEBSCSIDriverPolicy = "arn:aws:iam::aws:policy/service-role/AmazonEBSCSIDriverPolicy"
      }
    }
  }

  # Grants the IAM identity running terraform admin access to the cluster
  enable_cluster_creator_admin_permissions = true

  tags = {
    Name = var.cluster_name
  }
}

# -------------------------------------------------------------
# IRSA ROLE FOR EBS CSI DRIVER
# Required for the EBS CSI driver addon to provision PersistentVolumes
# -------------------------------------------------------------

module "ebs_csi_irsa_role" {
  source  = "terraform-aws-modules/iam/aws//modules/iam-role-for-service-accounts-eks"
  version = "~> 5.0"

  role_name             = "${var.cluster_name}-ebs-csi-role"
  attach_ebs_csi_policy = true

  oidc_providers = {
    main = {
      provider_arn               = module.eks.oidc_provider_arn
      namespace_service_accounts = ["kube-system:ebs-csi-controller-sa"]
    }
  }
}
