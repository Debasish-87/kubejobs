# -------------------------------------------------------------
# VPC MODULE
# -------------------------------------------------------------
# Creates a VPC with the following layout per availability zone:
#   - One public subnet  (load balancers)
#   - One private subnet (EKS nodes, Redis)
#   - One NAT gateway    (private subnet outbound internet access)
#
# Subnet tags required by EKS for load balancer discovery are
# applied automatically using the cluster_name variable.
# -------------------------------------------------------------

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name                                        = "kubejobs-vpc"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  }
}

# -------------------------------------------------------------
# INTERNET GATEWAY
# -------------------------------------------------------------

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id

  tags = { Name = "kubejobs-igw" }
}

# -------------------------------------------------------------
# SUBNETS
# -------------------------------------------------------------

resource "aws_subnet" "public" {
  count                   = length(var.azs)
  vpc_id                  = aws_vpc.main.id
  cidr_block              = cidrsubnet(var.vpc_cidr, 8, count.index)
  availability_zone       = var.azs[count.index]
  map_public_ip_on_launch = true

  tags = {
    Name                                        = "kubejobs-public-${var.azs[count.index]}"
    "kubernetes.io/role/elb"                    = "1"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  }
}

resource "aws_subnet" "private" {
  count             = length(var.azs)
  vpc_id            = aws_vpc.main.id
  cidr_block        = cidrsubnet(var.vpc_cidr, 8, count.index + 10)
  availability_zone = var.azs[count.index]

  tags = {
    Name                                        = "kubejobs-private-${var.azs[count.index]}"
    "kubernetes.io/role/internal-elb"           = "1"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  }
}

# -------------------------------------------------------------
# NAT GATEWAYS
# One NAT gateway per AZ for high availability.
# Each private subnet routes outbound traffic through its own NAT.
# -------------------------------------------------------------

resource "aws_eip" "nat" {
  count      = 1
  domain     = "vpc"
  depends_on = [aws_internet_gateway.main]

  tags = { Name = "kubejobs-nat-eip-${count.index}" }
}

resource "aws_nat_gateway" "main" {
  count         = 1
  allocation_id = aws_eip.nat[count.index].id
  subnet_id     = aws_subnet.public[count.index].id
  depends_on    = [aws_internet_gateway.main]

  tags = { Name = "kubejobs-nat-${count.index}" }
}

# -------------------------------------------------------------
# ROUTE TABLES
# -------------------------------------------------------------

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }

  tags = { Name = "kubejobs-public-rt" }
}

resource "aws_route_table" "private" {
  count  = length(var.azs)
  vpc_id = aws_vpc.main.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.main[0].id
  }

  tags = { Name = "kubejobs-private-rt-${count.index}" }
}

resource "aws_route_table_association" "public" {
  count          = length(var.azs)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "private" {
  count          = length(var.azs)
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private[count.index].id
}
