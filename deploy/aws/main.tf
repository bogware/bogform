# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0

terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.7"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
  }
}

locals {
  ssm_prefix = "/${var.name}"
  public_url = var.domain_name != null ? "https://${var.domain_name}" : "https://${aws_cloudfront_distribution.this.domain_name}"

  # An administrator sets these after deploying, so that the values never enter deployment state.
  secret_parameters = toset(concat(
    ["database-url", "session-key", "oidc-client-secret"],
    var.github_trigger ? ["github-token"] : [],
    var.gitlab_trigger ? ["gitlab-token"] : [],
    var.bitbucket_trigger ? ["bitbucket-token"] : [],
    var.azure_org_url != null ? ["azure-token"] : [],
    var.alert_slack ? ["alert-slack-webhook"] : [],
    var.alert_teams ? ["alert-teams-webhook"] : [],
    var.alert_webhook ? ["alert-webhook-url", "alert-webhook-secret"] : [],
  ))

  # SES checks permission on the sender's identity, and in its sandbox on each recipient's too. An
  # identity can be an address or its domain.
  alert_email_identities = var.alert_email_from == null ? [] : distinct(flatten([
    for address in concat([var.alert_email_from], var.alert_email_to) : [address, split("@", address)[1]]
  ]))

  content_types = {
    css   = "text/css; charset=utf-8"
    html  = "text/html; charset=utf-8"
    ico   = "image/x-icon"
    js    = "text/javascript; charset=utf-8"
    json  = "application/json"
    map   = "application/json"
    png   = "image/png"
    svg   = "image/svg+xml"
    txt   = "text/plain; charset=utf-8"
    woff2 = "font/woff2"
  }

  # Assets have content-hashed names, so they can be cached forever. index.html must stay fresh.
  ui_files = fileset("${var.artifacts_dir}/ui", "**")
}

data "aws_partition" "current" {}
data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

# Secrets

resource "aws_ssm_parameter" "secret" {
  for_each = local.secret_parameters

  name  = "${local.ssm_prefix}/${each.key}"
  type  = "SecureString"
  value = "change-me"

  lifecycle {
    ignore_changes = [value]
  }
}

resource "random_password" "origin_secret" {
  length  = 48
  special = false
}

resource "aws_ssm_parameter" "origin_secret" {
  name  = "${local.ssm_prefix}/origin-secret"
  type  = "SecureString"
  value = random_password.origin_secret.result
}

# This isn't secret. It's here rather than on the function because the function must exist before
# the distribution, whose address this contains.
resource "aws_ssm_parameter" "public_url" {
  name  = "${local.ssm_prefix}/public-url"
  type  = "String"
  value = local.public_url
}

# API

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "server" {
  name               = "${var.name}-server"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_cloudwatch_log_group" "server" {
  name              = "/aws/lambda/${var.name}-server"
  retention_in_days = var.log_retention_days
}

# The server can write its logs and read its own settings, and nothing else in the account. In
# particular, it has no access to any infrastructure.
data "aws_iam_policy_document" "server" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.server.arn}:*"]
  }
  statement {
    actions   = ["ssm:GetParametersByPath"]
    resources = ["arn:${data.aws_partition.current.partition}:ssm:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:parameter${local.ssm_prefix}", "arn:${data.aws_partition.current.partition}:ssm:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:parameter${local.ssm_prefix}/*"]
  }

  # Email alerts, only as the configured sender.
  dynamic "statement" {
    for_each = length(local.alert_email_identities) > 0 ? [1] : []
    content {
      actions   = ["ses:SendEmail"]
      resources = [for identity in local.alert_email_identities : "arn:${data.aws_partition.current.partition}:ses:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:identity/${identity}"]
      condition {
        test     = "StringEquals"
        variable = "ses:FromAddress"
        values   = [var.alert_email_from]
      }
    }
  }
}

resource "aws_iam_role_policy" "server" {
  name   = "server"
  role   = aws_iam_role.server.id
  policy = data.aws_iam_policy_document.server.json
}

data "archive_file" "server" {
  type        = "zip"
  source_file = "${var.artifacts_dir}/bootstrap"
  output_path = "${path.module}/.build/${var.name}-server.zip"
}

resource "aws_lambda_function" "server" {
  function_name    = "${var.name}-server"
  role             = aws_iam_role.server.arn
  filename         = data.archive_file.server.output_path
  source_code_hash = data.archive_file.server.output_base64sha256
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  memory_size      = 256
  timeout          = 30

  # Limits both the most this could cost during a flood of requests, and how many database connections
  # it can open. Low-cost Postgres plans allow few.
  reserved_concurrent_executions = var.max_concurrency

  environment {
    variables = merge(
      {
        BOG_SERVER_SSM_PREFIX     = local.ssm_prefix
        BOG_SERVER_AUTH_PROVIDER  = var.auth_provider
        BOG_SERVER_OIDC_ISSUER    = coalesce(var.oidc_issuer, "unused")
        BOG_SERVER_OIDC_CLIENT_ID = var.oidc_client_id
        BOG_SERVER_VIEWERS        = join(",", var.viewers)
        BOG_SERVER_OPERATORS      = join(",", var.operators)
        BOG_SERVER_RETENTION_DAYS = tostring(var.retention_days)
        BOG_SERVER_TRIGGER_REF    = var.trigger_ref
      },
      var.oidc_trust_email ? { BOG_SERVER_OIDC_TRUST_EMAIL = "true" } : {},
      var.azure_org_url != null ? { BOG_SERVER_AZURE_ORG_URL = var.azure_org_url } : {},
      length(var.azure_pipelines) > 0 ? {
        BOG_SERVER_AZURE_PIPELINES = join(",", [for repository, pipeline in var.azure_pipelines : "${repository}=${pipeline}"])
      } : {},
      var.alert_email_from != null ? {
        BOG_SERVER_ALERT_EMAIL_FROM = var.alert_email_from
        BOG_SERVER_ALERT_EMAIL_TO   = join(",", var.alert_email_to)
      } : {},
      length(var.alert_events) > 0 ? { BOG_SERVER_ALERT_EVENTS = join(",", var.alert_events) } : {},
      length(var.alert_repositories) > 0 ? { BOG_SERVER_ALERT_REPOSITORIES = join(",", var.alert_repositories) } : {},
    )
  }

  depends_on = [aws_cloudwatch_log_group.server, aws_iam_role_policy.server]
}

# Access is public at the network level, because CloudFront can't sign POST bodies for a function URL
# without help from each client. Instead, the server refuses any request lacking the origin secret,
# which only CloudFront sends.
resource "aws_lambda_function_url" "server" {
  function_name      = aws_lambda_function.server.function_name
  authorization_type = "NONE"
}

resource "aws_lambda_permission" "url" {
  statement_id           = "AllowFunctionUrl"
  action                 = "lambda:InvokeFunctionUrl"
  function_name          = aws_lambda_function.server.function_name
  principal              = "*"
  function_url_auth_type = "NONE"
}

resource "aws_lambda_permission" "invoke" {
  statement_id  = "AllowInvokeThroughFunctionUrl"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.server.function_name
  principal     = "*"
  # Without this condition, anyone could invoke the function directly.
  invoked_via_function_url = true
}

# UI

resource "aws_s3_bucket" "ui" {
  bucket_prefix = "${var.name}-ui-"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "ui" {
  bucket                  = aws_s3_bucket.ui.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_object" "ui" {
  for_each = local.ui_files

  bucket        = aws_s3_bucket.ui.id
  key           = each.value
  source        = "${var.artifacts_dir}/ui/${each.value}"
  etag          = filemd5("${var.artifacts_dir}/ui/${each.value}")
  content_type  = lookup(local.content_types, reverse(split(".", each.value))[0], "application/octet-stream")
  cache_control = startswith(each.value, "assets/") ? "public, max-age=31536000, immutable" : "no-cache"
}

data "aws_iam_policy_document" "ui" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.ui.arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "ui" {
  bucket     = aws_s3_bucket.ui.id
  policy     = data.aws_iam_policy_document.ui.json
  depends_on = [aws_s3_bucket_public_access_block.ui]
}

# CDN

resource "aws_cloudfront_origin_access_control" "ui" {
  name                              = "${var.name}-ui"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# Sends paths without a file extension to index.html, because the UI handles its own routes.
resource "aws_cloudfront_function" "spa" {
  name    = "${var.name}-spa"
  runtime = "cloudfront-js-2.0"
  publish = true
  code    = <<-EOT
    function handler(event) {
      var request = event.request;
      if (!request.uri.split('/').pop().includes('.')) {
        request.uri = '/index.html';
      }
      return request;
    }
  EOT
}

resource "aws_cloudfront_response_headers_policy" "ui" {
  name = "${var.name}-ui"

  security_headers_config {
    content_security_policy {
      content_security_policy = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
      override                = true
    }
    content_type_options {
      override = true
    }
    frame_options {
      frame_option = "DENY"
      override     = true
    }
    referrer_policy {
      referrer_policy = "no-referrer"
      override        = true
    }
    strict_transport_security {
      access_control_max_age_sec = 31536000
      include_subdomains         = true
      override                   = true
    }
  }
}

# AWS's managed policies.
locals {
  cache_policy_optimized         = "658327ea-f89d-4fab-a63d-7e88639e58f6"
  cache_policy_disabled          = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  origin_request_all_except_host = "b689b0a8-53d0-40ab-baf2-68738e2966ac"
  api_origin_id                  = "api"
  ui_origin_id                   = "ui"
}

resource "aws_cloudfront_distribution" "this" {
  enabled             = true
  comment             = var.name
  default_root_object = "index.html"
  http_version        = "http2and3"
  is_ipv6_enabled     = true
  price_class         = var.price_class
  aliases             = var.domain_name != null ? [var.domain_name] : []

  origin {
    origin_id                = local.ui_origin_id
    domain_name              = aws_s3_bucket.ui.bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.ui.id
  }

  origin {
    origin_id   = local.api_origin_id
    domain_name = split("/", aws_lambda_function_url.server.function_url)[2]

    custom_origin_config {
      http_port              = 80
      https_port             = 443
      origin_protocol_policy = "https-only"
      origin_ssl_protocols   = ["TLSv1.2"]
    }

    custom_header {
      name  = "X-Bogform-Origin-Secret"
      value = random_password.origin_secret.result
    }
  }

  default_cache_behavior {
    target_origin_id           = local.ui_origin_id
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = local.cache_policy_optimized
    response_headers_policy_id = aws_cloudfront_response_headers_policy.ui.id

    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.spa.arn
    }
  }

  ordered_cache_behavior {
    path_pattern             = "/api/*"
    target_origin_id         = local.api_origin_id
    viewer_protocol_policy   = "https-only"
    allowed_methods          = ["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"]
    cached_methods           = ["GET", "HEAD"]
    compress                 = true
    cache_policy_id          = local.cache_policy_disabled
    origin_request_policy_id = local.origin_request_all_except_host
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = var.domain_name == null
    acm_certificate_arn            = var.certificate_arn
    ssl_support_method             = var.domain_name != null ? "sni-only" : null
    minimum_protocol_version       = var.domain_name != null ? "TLSv1.2_2021" : null
  }
}
