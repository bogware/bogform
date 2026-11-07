// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package adopt

import (
	"fmt"
	"strings"
)

// ARN is a parsed Amazon Resource Name.
type ARN struct {
	Partition, Service, Region, Account, Resource string
}

// ParseARN splits an ARN into its parts.
func ParseARN(s string) (ARN, error) {
	parts := strings.SplitN(s, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return ARN{}, fmt.Errorf("%q isn't an ARN", s)
	}
	return ARN{Partition: parts[1], Service: parts[2], Region: parts[3], Account: parts[4], Resource: parts[5]}, nil
}

// resourceParts splits an ARN's resource into its type and ID. Resources are "type/id",
// "type:id", or just "id".
func resourceParts(resource string) (kind, id string) {
	if i := strings.IndexAny(resource, "/:"); i >= 0 {
		return resource[:i], resource[i+1:]
	}
	return "", resource
}

// lastSegment returns what follows the last "/".
func lastSegment(s string) string {
	return s[strings.LastIndex(s, "/")+1:]
}

// arnMapping maps one kind of ARN to a resource type and import ID.
type arnMapping struct {
	service, kind string
	tfType        string
	// importID builds the import ID. If it's nil, the ARN's resource ID is used.
	importID func(a ARN, id string) string
	// name builds the readable name. If it's nil, the import ID is used.
	name func(a ARN, id string) string
	note string
}

func fullARN(a ARN, _ string) string {
	return "arn:" + a.Partition + ":" + a.Service + ":" + a.Region + ":" + a.Account + ":" + a.Resource
}

func lastOfID(_ ARN, id string) string { return lastSegment(id) }

// arnMappings covers the services that adopt supports. Keys are service and resource kind.
var arnMappings = []arnMapping{
	{service: "s3", kind: "", tfType: "aws_s3_bucket"},
	{service: "cloudfront", kind: "distribution", tfType: "aws_cloudfront_distribution"},
	{service: "lambda", kind: "function", tfType: "aws_lambda_function", importID: func(_ ARN, id string) string {
		name, _, _ := strings.Cut(id, ":") // Drop any version or alias.
		return name
	}},
	{service: "iam", kind: "role", tfType: "aws_iam_role", importID: lastOfID},
	{service: "iam", kind: "policy", tfType: "aws_iam_policy", importID: fullARN, name: lastOfID},
	{service: "iam", kind: "user", tfType: "aws_iam_user", importID: lastOfID},
	{service: "dynamodb", kind: "table", tfType: "aws_dynamodb_table"},
	{service: "logs", kind: "log-group", tfType: "aws_cloudwatch_log_group", importID: func(_ ARN, id string) string {
		return strings.TrimSuffix(id, ":*")
	}},
	{service: "route53", kind: "hostedzone", tfType: "aws_route53_zone"},
	{service: "acm", kind: "certificate", tfType: "aws_acm_certificate", importID: fullARN, name: lastOfID},
	{service: "sqs", kind: "", tfType: "aws_sqs_queue", importID: func(a ARN, id string) string {
		host := "sqs." + a.Region + ".amazonaws.com"
		if a.Partition == "aws-cn" {
			host += ".cn"
		}
		return "https://" + host + "/" + a.Account + "/" + id
	}, name: func(_ ARN, id string) string { return id }},
	{service: "sns", kind: "", tfType: "aws_sns_topic", importID: fullARN, name: func(_ ARN, id string) string { return id }},
	{service: "ec2", kind: "instance", tfType: "aws_instance"},
	{service: "ec2", kind: "vpc", tfType: "aws_vpc"},
	{service: "ec2", kind: "subnet", tfType: "aws_subnet"},
	{service: "ec2", kind: "security-group", tfType: "aws_security_group"},
	{service: "ec2", kind: "internet-gateway", tfType: "aws_internet_gateway"},
	{service: "ec2", kind: "natgateway", tfType: "aws_nat_gateway"},
	{service: "ec2", kind: "route-table", tfType: "aws_route_table"},
	{service: "ec2", kind: "elastic-ip", tfType: "aws_eip"},
	{service: "ec2", kind: "volume", tfType: "aws_ebs_volume"},
	{service: "rds", kind: "db", tfType: "aws_db_instance"},
	{service: "rds", kind: "cluster", tfType: "aws_rds_cluster"},
	{service: "ecr", kind: "repository", tfType: "aws_ecr_repository"},
	{service: "secretsmanager", kind: "secret", tfType: "aws_secretsmanager_secret", importID: fullARN, name: func(_ ARN, id string) string {
		// Secrets Manager adds a hyphen and six random characters to the name.
		if i := strings.LastIndex(id, "-"); i > 0 && len(id)-i == 7 {
			return id[:i]
		}
		return id
	}, note: "Only the secret is imported, not its value."},
	{service: "ses", kind: "identity", tfType: "aws_sesv2_email_identity"},
	{service: "kms", kind: "key", tfType: "aws_kms_key"},
	{service: "states", kind: "stateMachine", tfType: "aws_sfn_state_machine", importID: fullARN, name: lastOfID},
	{service: "events", kind: "rule", tfType: "aws_cloudwatch_event_rule", importID: func(_ ARN, id string) string {
		// Rules on the default bus are imported by name, and others as "bus/name".
		if bus, name, found := strings.Cut(id, "/"); found {
			return bus + "/" + name
		}
		return id
	}, name: lastOfID},
	{service: "elasticloadbalancing", kind: "loadbalancer", tfType: "aws_lb", importID: fullARN, name: func(_ ARN, id string) string {
		parts := strings.Split(id, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2]
		}
		return id
	}},
	{service: "ecs", kind: "cluster", tfType: "aws_ecs_cluster"},
	{service: "ecs", kind: "service", tfType: "aws_ecs_service", name: lastOfID},
	{service: "iam", kind: "oidc-provider", tfType: "aws_iam_openid_connect_provider", importID: fullARN, name: func(_ ARN, id string) string { return id }},
	{service: "budgets", kind: "budget", tfType: "aws_budgets_budget", importID: func(a ARN, id string) string { return a.Account + ":" + id }},
	{service: "elasticloadbalancing", kind: "listener", tfType: "aws_lb_listener", importID: fullARN, name: lbChildName},
	{service: "elasticloadbalancing", kind: "listener-rule", tfType: "aws_lb_listener_rule", importID: fullARN, name: lbChildName},
	{service: "elasticloadbalancing", kind: "targetgroup", tfType: "aws_lb_target_group", importID: fullARN, name: func(_ ARN, id string) string {
		name, _, _ := strings.Cut(id, "/")
		return name
	}},
	{service: "cloudwatch", kind: "alarm", tfType: "aws_cloudwatch_metric_alarm"},
	{service: "ssm", kind: "parameter", tfType: "aws_ssm_parameter", importID: func(_ ARN, id string) string { return "/" + strings.TrimPrefix(id, "/") }, note: "The parameter's value is read into the state, so make sure the state is encrypted."},
	{service: "elasticfilesystem", kind: "file-system", tfType: "aws_efs_file_system"},
}

// lbChildName names a listener or rule after its load balancer, from IDs like
// "app/name/lb-id/listener-id/rule-id".
func lbChildName(_ ARN, id string) string {
	parts := strings.Split(id, "/")
	if len(parts) >= 2 {
		return parts[1] + "_" + parts[len(parts)-1]
	}
	return id
}

// MapARN maps an ARN to the resource that manages it. ok is false if adopt doesn't support it,
// with the reason.
func MapARN(s string) (r Resource, ok bool, reason string) {
	a, err := ParseARN(s)
	if err != nil {
		return Resource{}, false, err.Error()
	}
	if a.Service == "apigateway" {
		return mapAPIGateway(a, s)
	}
	kind, id := resourceParts(a.Resource)
	if a.Service == "s3" || a.Service == "sqs" || a.Service == "sns" {
		kind, id = "", a.Resource
		if a.Service == "s3" && strings.Contains(id, "/") {
			return Resource{}, false, "S3 objects aren't imported"
		}
	}
	for _, m := range arnMappings {
		if m.service != a.Service || m.kind != kind {
			continue
		}
		importID := id
		if m.importID != nil {
			importID = m.importID(a, id)
		}
		name := importID
		if m.name != nil {
			name = m.name(a, id)
		}
		return Resource{Type: m.tfType, ImportID: importID, Name: name, Source: s, Note: m.note}, true, ""
	}
	if kind == "" {
		return Resource{}, false, fmt.Sprintf("adopt doesn't support %s resources yet", a.Service)
	}
	return Resource{}, false, fmt.Sprintf("adopt doesn't support %s %s resources yet", a.Service, kind)
}

// mapAPIGateway handles API Gateway, whose ARNs are paths like "/apis/abc123" or
// "/restapis/abc123/stages/prod".
func mapAPIGateway(a ARN, s string) (Resource, bool, string) {
	parts := strings.Split(strings.TrimPrefix(a.Resource, "/"), "/")
	switch {
	case len(parts) == 2 && parts[0] == "apis":
		return Resource{Type: "aws_apigatewayv2_api", ImportID: parts[1], Name: parts[1], Source: s,
			Note: "Routes, integrations and stages aren't discovered. Add their import blocks with IDs like \"<api id>/<route id>\"."}, true, ""
	case len(parts) == 2 && parts[0] == "restapis":
		return Resource{Type: "aws_api_gateway_rest_api", ImportID: parts[1], Name: parts[1], Source: s}, true, ""
	case len(parts) == 4 && parts[0] == "apis" && parts[2] == "stages":
		return Resource{Type: "aws_apigatewayv2_stage", ImportID: parts[1] + "/" + parts[3], Name: parts[1] + "_" + parts[3], Source: s}, true, ""
	case len(parts) == 4 && parts[0] == "restapis" && parts[2] == "stages":
		return Resource{Type: "aws_api_gateway_stage", ImportID: parts[1] + "/" + parts[3], Name: parts[1] + "_" + parts[3], Source: s}, true, ""
	}
	return Resource{}, false, "adopt doesn't support this kind of API Gateway resource yet"
}

// cloudControlType describes a Cloud Control resource type that adopt lists for -prefix.
type cloudControlType struct {
	// Name is the CloudFormation type name, such as AWS::S3::Bucket.
	Name string
	// Global types are listed in us-east-1 only.
	Global bool
	// toARN turns a primary identifier into an ARN, so that MapARN can handle it.
	toARN func(region, account, identifier string) string
	// nameProperty is the property holding the object's name, when the identifier isn't the name.
	nameProperty string
}

// CloudControlTypes are listed for -prefix. Types whose IDs are generated, such as VPCs, are only
// found with -tag.
var CloudControlTypes = []cloudControlType{
	{Name: "AWS::S3::Bucket", Global: true, toARN: func(_, _, id string) string { return "arn:aws:s3:::" + id }},
	{Name: "AWS::Lambda::Function", toARN: func(r, a, id string) string { return "arn:aws:lambda:" + r + ":" + a + ":function:" + id }},
	{Name: "AWS::IAM::Role", Global: true, toARN: func(_, a, id string) string { return "arn:aws:iam::" + a + ":role/" + id }},
	{Name: "AWS::DynamoDB::Table", toARN: func(r, a, id string) string { return "arn:aws:dynamodb:" + r + ":" + a + ":table/" + id }},
	{Name: "AWS::Logs::LogGroup", toARN: func(r, a, id string) string { return "arn:aws:logs:" + r + ":" + a + ":log-group:" + id }},
	{Name: "AWS::SQS::Queue", toARN: func(r, a, id string) string { return "arn:aws:sqs:" + r + ":" + a + ":" + lastSegment(id) }},
	{Name: "AWS::SNS::Topic", toARN: func(_, _, id string) string { return id }},
	{Name: "AWS::ECR::Repository", toARN: func(r, a, id string) string { return "arn:aws:ecr:" + r + ":" + a + ":repository/" + id }},
	{Name: "AWS::RDS::DBInstance", toARN: func(r, a, id string) string { return "arn:aws:rds:" + r + ":" + a + ":db:" + id }},
	{Name: "AWS::SecretsManager::Secret", toARN: func(_, _, id string) string { return id }},
	{Name: "AWS::SES::EmailIdentity", toARN: func(r, a, id string) string { return "arn:aws:ses:" + r + ":" + a + ":identity/" + id }},
	{Name: "AWS::ApiGatewayV2::Api", toARN: func(r, _, id string) string { return "arn:aws:apigateway:" + r + "::/apis/" + id }, nameProperty: "Name"},
	{Name: "AWS::Route53::HostedZone", Global: true, toARN: func(_, _, id string) string { return "arn:aws:route53:::hostedzone/" + id }, nameProperty: "Name"},
	{Name: "AWS::StepFunctions::StateMachine", toARN: func(_, _, id string) string { return id }},
}

// MapCloudControl maps a Cloud Control identifier to a resource. name is the object's name from
// its properties, if the type has a name property. It returns the name that -prefix should match.
func MapCloudControl(t cloudControlType, region, account, identifier, name string) (r Resource, matchName string, ok bool, reason string) {
	arn := t.toARN(region, account, identifier)
	r, ok, reason = MapARN(arn)
	if !ok {
		return r, "", false, reason
	}
	matchName = r.Name
	if t.nameProperty != "" && name != "" {
		r.Name = strings.TrimSuffix(name, ".")
		matchName = r.Name
	}
	return r, matchName, true, ""
}
