output "cluster_name" {
  value = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "EKS API server endpoint"
  value       = module.eks.cluster_endpoint
}

output "cluster_certificate_authority_data" {
  description = "Base64-encoded certificate authority data for the cluster"
  value       = module.eks.cluster_certificate_authority_data
}

output "oidc_provider_arn" {
  description = "ARN of the OIDC provider. Required for creating IRSA roles"
  value       = module.eks.oidc_provider_arn
}

output "oidc_issuer" {
  description = "OIDC issuer URL. Used in IRSA trust policy conditions"
  value       = module.eks.cluster_oidc_issuer_url
}
