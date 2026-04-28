# KubeJobs — Terraform Infrastructure

## Architecture

```
AWS (ap-south-1)
├── VPC  10.0.0.0/16
│   ├── Public Subnets  x2 AZ   Load balancers
│   ├── Private Subnets x2 AZ   EKS nodes, Redis
│   ├── Internet Gateway
│   ├── NAT Gateway     x2 AZ   Outbound access for private subnets
│   └── Route Tables
│
├── EKS Cluster  v1.29
│   ├── Managed Node Group  t3.medium, 1-5 nodes
│   └── Addons
│       ├── CoreDNS
│       ├── kube-proxy
│       ├── vpc-cni
│       └── aws-ebs-csi-driver
│
├── ElastiCache Redis  7.1
│   ├── 2 nodes, Multi-AZ, automatic failover
│   ├── Encryption at rest and in transit
│   └── Daily snapshots, 7-day retention
│
├── ECR Repository
│   ├── Scan on push enabled
│   └── Lifecycle policy: keep 10 most recent images
│
└── IAM
    ├── EKS cluster and node roles  managed by eks module
    ├── EBS CSI driver IRSA role
    └── App IRSA role  SQS and S3 access for pods
```

## Prerequisites

- Terraform >= 1.5.0
- AWS CLI configured with credentials
- kubectl
- docker

## Deployment

### Step 1 — Create the remote state backend (one time only)

```bash
aws s3api create-bucket \
  --bucket kubejobs-terraform-state \
  --region ap-south-1 \
  --create-bucket-configuration LocationConstraint=ap-south-1

aws s3api put-bucket-versioning \
  --bucket kubejobs-terraform-state \
  --versioning-configuration Status=Enabled

aws dynamodb create-table \
  --table-name kubejobs-terraform-locks \
  --attribute-definitions AttributeName=LockID,AttributeType=S \
  --key-schema AttributeName=LockID,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST \
  --region ap-south-1
```

### Step 2 — Configure variables

```bash
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars` with your values.

### Step 3 — Initialize and deploy

```bash
terraform init

# Apply VPC and EKS first. The kubernetes and helm providers
# require the cluster endpoint to be available before planning
# resources that depend on them.
terraform apply -target=module.vpc -target=module.eks

# Apply everything else
terraform apply
```

### Step 4 — Configure kubectl

```bash
aws eks update-kubeconfig --region ap-south-1 --name kubejobs
kubectl get nodes
```

### Step 5 — Push a Docker image to ECR

```bash
ECR_URL=$(terraform output -raw ecr_repository_url)
REGISTRY=$(echo $ECR_URL | cut -d/ -f1)

aws ecr get-login-password --region ap-south-1 | \
  docker login --username AWS --password-stdin $REGISTRY

docker build -t kubejobs .
docker tag kubejobs:latest $ECR_URL:latest
docker push $ECR_URL:latest
```

### Step 6 — Pass Redis endpoint to the application

```bash
REDIS_HOST=$(terraform output -raw redis_endpoint)

kubectl create secret generic kubejobs-secrets \
  --from-literal=REDIS_URL="redis://${REDIS_HOST}:6379"
```

### Step 7 — Annotate the ServiceAccount for IRSA (SQS and S3 access)

```bash
ROLE_ARN=$(terraform output -raw kubejobs_irsa_role_arn)

kubectl annotate serviceaccount kubejobs-sa \
  eks.amazonaws.com/role-arn=$ROLE_ARN
```

## Teardown

```bash
terraform destroy
```
