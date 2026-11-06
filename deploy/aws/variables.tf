# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0

variable "name" {
  type        = string
  description = "A name for this deployment, used to name its resources."
  default     = "bogform"

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,30}$", var.name))
    error_message = "Use up to 31 lowercase letters, digits, and hyphens."
  }
}

variable "artifacts_dir" {
  type        = string
  description = "A directory containing the server built as \"bootstrap\" for linux/arm64, and the UI in \"ui\". scripts/build-server.sh creates it."
}

variable "viewers" {
  type        = list(string)
  description = "Email addresses or domains, such as \"@example.com\", of people who can view the dashboard."
  default     = []
}

variable "operators" {
  type        = list(string)
  description = "Email addresses or domains of people who can also start pipelines."
  default     = []
}

variable "auth_provider" {
  type        = string
  description = "How people sign in: \"oidc\" or \"github\"."
  default     = "oidc"

  validation {
    condition     = contains(["oidc", "github"], var.auth_provider)
    error_message = "Must be oidc or github."
  }
}

variable "oidc_issuer" {
  type        = string
  description = "The OIDC provider's issuer URL. Not used for GitHub."
  default     = null
}

variable "oidc_client_id" {
  type        = string
  description = "The client ID from your sign-in provider. Set the client secret in the oidc-client-secret parameter."
}

variable "oidc_trust_email" {
  type        = bool
  description = "Accept email addresses that the provider doesn't mark as verified. Only use this with a single-organization provider that controls its users' addresses, such as Microsoft Entra ID."
  default     = false
}

variable "domain_name" {
  type        = string
  description = "A custom domain for the dashboard. If unset, it uses its CloudFront address. Register that address as the redirect URL with your sign-in provider."
  default     = null
}

variable "certificate_arn" {
  type        = string
  description = "An ACM certificate in us-east-1 for domain_name."
  default     = null
}

variable "trigger_ref" {
  type        = string
  description = "The branch that the dashboard starts pipelines on."
  default     = "main"
}

variable "github_trigger" {
  type        = bool
  description = "Let operators start GitHub Actions workflows. Set the token in the github-token parameter."
  default     = false
}

variable "gitlab_trigger" {
  type        = bool
  description = "Let operators start GitLab pipelines. Set the token in the gitlab-token parameter."
  default     = false
}

variable "bitbucket_trigger" {
  type        = bool
  description = "Let operators start Bitbucket pipelines. Set the token in the bitbucket-token parameter."
  default     = false
}

variable "azure_org_url" {
  type        = string
  description = "Let operators start Azure pipelines in this organization, such as https://dev.azure.com/example. Set the token in the azure-token parameter."
  default     = null
}

variable "azure_pipelines" {
  type        = map(string)
  description = "Maps Azure repository names to \"project/pipelineID\"."
  default     = {}
}

variable "retention_days" {
  type        = number
  description = "How many days to keep runs. Use 0 to keep them forever."
  default     = 365
}

variable "log_retention_days" {
  type        = number
  description = "How many days to keep logs."
  default     = 30
}

variable "max_concurrency" {
  type        = number
  description = "The most requests to handle at once. This limits costs and database connections."
  default     = 10
}

variable "price_class" {
  type        = string
  description = "The CloudFront price class. The default is the cheapest, and is slower for people far from North America and Europe."
  default     = "PriceClass_100"
}
