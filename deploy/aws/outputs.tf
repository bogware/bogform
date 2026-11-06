# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0

output "url" {
  description = "The dashboard's address. Set this as BOG_SERVER_URL in your pipelines."
  value       = local.public_url
}

output "redirect_url" {
  description = "Register this with your sign-in provider."
  value       = "${local.public_url}/api/v1/auth/callback"
}

output "secret_parameters" {
  description = "The SSM parameters that you must set after deploying."
  value       = [for parameter in aws_ssm_parameter.secret : parameter.name]
}

output "log_group" {
  description = "Where the server writes its logs."
  value       = aws_cloudwatch_log_group.server.name
}
