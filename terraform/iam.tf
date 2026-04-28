# -------------------------------------------------------------
# IAM — APPLICATION-LEVEL ROLES
# -------------------------------------------------------------
# The EKS cluster and node group IAM roles are managed by the
# terraform-aws-modules/eks module and do not need to be defined here.
#
# This file provisions an IRSA role (IAM Role for Service Accounts)
# that grants pods in the kubejobs namespace scoped access to
# SQS and S3 without needing static credentials in the container.
# -------------------------------------------------------------

resource "aws_iam_role" "kubejobs_app" {
  name = "kubejobs-app-irsa-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Federated = module.eks.oidc_provider_arn
      }
      Action = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${module.eks.oidc_issuer}:sub" = "system:serviceaccount:kubejobs:kubejobs-sa"
          "${module.eks.oidc_issuer}:aud" = "sts.amazonaws.com"
        }
      }
    }]
  })
}

# SQS permissions for job queue operations
resource "aws_iam_policy" "kubejobs_sqs" {
  name        = "kubejobs-sqs-policy"
  description = "Allows kubejobs pods to send, receive, and delete SQS messages"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "sqs:SendMessage",
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:GetQueueAttributes",
        "sqs:GetQueueUrl"
      ]
      Resource = "arn:aws:sqs:${var.region}:*:kubejobs-*"
    }]
  })
}

# S3 permissions for job artifact storage
resource "aws_iam_policy" "kubejobs_s3" {
  name        = "kubejobs-s3-policy"
  description = "Allows kubejobs pods to read and write job artifacts to S3"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:ListBucket"
      ]
      Resource = [
        "arn:aws:s3:::kubejobs-artifacts",
        "arn:aws:s3:::kubejobs-artifacts/*"
      ]
    }]
  })
}

resource "aws_iam_role_policy_attachment" "kubejobs_sqs" {
  role       = aws_iam_role.kubejobs_app.name
  policy_arn = aws_iam_policy.kubejobs_sqs.arn
}

resource "aws_iam_role_policy_attachment" "kubejobs_s3" {
  role       = aws_iam_role.kubejobs_app.name
  policy_arn = aws_iam_policy.kubejobs_s3.arn
}

output "kubejobs_irsa_role_arn" {
  description = "IRSA role ARN. Annotate the Kubernetes ServiceAccount with this value"
  value       = aws_iam_role.kubejobs_app.arn
}
