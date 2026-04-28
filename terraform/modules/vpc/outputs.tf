output "vpc_id" {
  value = aws_vpc.main.id
}

output "public_subnets" {
  value = aws_subnet.public[*].id
}

output "private_subnets" {
  value = aws_subnet.private[*].id
}

output "nat_gateway_ips" {
  description = "Public IPs of NAT gateways. Whitelist these on external services if needed"
  value       = aws_eip.nat[*].public_ip
}
