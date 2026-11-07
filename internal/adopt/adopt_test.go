// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package adopt

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	taggingtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

func TestMapARN(t *testing.T) {
	tests := []struct {
		arn, tfType, id, name string
	}{
		{"arn:aws:s3:::my-site", "aws_s3_bucket", "my-site", "my-site"},
		{"arn:aws:cloudfront::123456789012:distribution/E1XZ0PDLUVQPGH", "aws_cloudfront_distribution", "E1XZ0PDLUVQPGH", "E1XZ0PDLUVQPGH"},
		{"arn:aws:lambda:us-east-1:123456789012:function:app-forms", "aws_lambda_function", "app-forms", "app-forms"},
		{"arn:aws:lambda:us-east-1:123456789012:function:app-forms:live", "aws_lambda_function", "app-forms", "app-forms"},
		{"arn:aws:iam::123456789012:role/service-role/app-runner", "aws_iam_role", "app-runner", "app-runner"},
		{"arn:aws:iam::123456789012:policy/app-deploy", "aws_iam_policy", "arn:aws:iam::123456789012:policy/app-deploy", "app-deploy"},
		{"arn:aws:dynamodb:us-east-1:123456789012:table/app-community", "aws_dynamodb_table", "app-community", "app-community"},
		{"arn:aws:logs:us-east-1:123456789012:log-group:/aws/lambda/app-forms:*", "aws_cloudwatch_log_group", "/aws/lambda/app-forms", "/aws/lambda/app-forms"},
		{"arn:aws:apigateway:us-east-1::/apis/jfbtt35rha", "aws_apigatewayv2_api", "jfbtt35rha", "jfbtt35rha"},
		{"arn:aws:apigateway:us-east-1::/apis/jfbtt35rha/stages/$default", "aws_apigatewayv2_stage", "jfbtt35rha/$default", "jfbtt35rha_$default"},
		{"arn:aws:apigateway:us-east-1::/restapis/abc/stages/prod", "aws_api_gateway_stage", "abc/prod", "abc_prod"},
		{"arn:aws:route53:::hostedzone/Z03100031XD01C21SDH23", "aws_route53_zone", "Z03100031XD01C21SDH23", "Z03100031XD01C21SDH23"},
		{"arn:aws:acm:us-east-1:123456789012:certificate/5d64217f", "aws_acm_certificate", "arn:aws:acm:us-east-1:123456789012:certificate/5d64217f", "5d64217f"},
		{"arn:aws:sqs:us-east-1:123456789012:jobs", "aws_sqs_queue", "https://sqs.us-east-1.amazonaws.com/123456789012/jobs", "jobs"},
		{"arn:aws:sns:us-east-1:123456789012:alerts", "aws_sns_topic", "arn:aws:sns:us-east-1:123456789012:alerts", "alerts"},
		{"arn:aws:ec2:us-east-1:123456789012:vpc/vpc-0abc", "aws_vpc", "vpc-0abc", "vpc-0abc"},
		{"arn:aws:ec2:us-east-1:123456789012:security-group/sg-1", "aws_security_group", "sg-1", "sg-1"},
		{"arn:aws:rds:us-east-1:123456789012:db:app-db", "aws_db_instance", "app-db", "app-db"},
		{"arn:aws:secretsmanager:us-east-1:123456789012:secret:app/api-key-AbC123", "aws_secretsmanager_secret", "arn:aws:secretsmanager:us-east-1:123456789012:secret:app/api-key-AbC123", "app/api-key"},
		{"arn:aws:ses:us-east-1:123456789012:identity/example.org", "aws_sesv2_email_identity", "example.org", "example.org"},
		{"arn:aws:events:us-east-1:123456789012:rule/nightly", "aws_cloudwatch_event_rule", "nightly", "nightly"},
		{"arn:aws:events:us-east-1:123456789012:rule/custom-bus/nightly", "aws_cloudwatch_event_rule", "custom-bus/nightly", "nightly"},
		{"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/50dc6c", "aws_lb", "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/50dc6c", "web"},
		{"arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com", "aws_iam_openid_connect_provider", "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com", "token.actions.githubusercontent.com"},
		{"arn:aws:budgets::123456789012:budget/monthly", "aws_budgets_budget", "123456789012:monthly", "123456789012:monthly"},
		{"arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/web/50dc6c/f1/r9", "aws_lb_listener_rule", "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/web/50dc6c/f1/r9", "web_r9"},
		{"arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web-tg/73e2d6", "aws_lb_target_group", "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web-tg/73e2d6", "web-tg"},
		{"arn:aws:ecs:us-east-1:123456789012:service/main/api", "aws_ecs_service", "main/api", "api"},
		{"arn:aws:ssm:us-east-1:123456789012:parameter/app/db-url", "aws_ssm_parameter", "/app/db-url", "/app/db-url"},
	}
	for _, tt := range tests {
		r, ok, reason := MapARN(tt.arn)
		if !ok {
			t.Errorf("%s: not mapped: %s", tt.arn, reason)
			continue
		}
		if r.Type != tt.tfType || r.ImportID != tt.id || r.Name != tt.name {
			t.Errorf("%s: got %s %q %q, want %s %q %q", tt.arn, r.Type, r.ImportID, r.Name, tt.tfType, tt.id, tt.name)
		}
	}

	for _, arn := range []string{"arn:aws:s3:::bucket/object.txt", "arn:aws:glue:us-east-1:123456789012:job/x", "not-an-arn"} {
		if _, ok, reason := MapARN(arn); ok || reason == "" {
			t.Errorf("%s: expected it to be unsupported, with a reason", arn)
		}
	}
}

func TestMapAzure(t *testing.T) {
	sub := "/subscriptions/0000/resourcegroups/web/providers/"
	tests := []struct {
		id, typ, name, kind, tfType, wantID string
	}{
		{sub + "Microsoft.Network/dnszones/example.com", "Microsoft.Network/dnszones", "example.com", "", "azurerm_dns_zone",
			"/subscriptions/0000/resourceGroups/web/providers/Microsoft.Network/dnsZones/example.com"},
		{sub + "Microsoft.Web/staticSites/apex", "Microsoft.Web/staticSites", "apex", "", "azurerm_static_web_app",
			"/subscriptions/0000/resourceGroups/web/providers/Microsoft.Web/staticSites/apex"},
		{sub + "Microsoft.Web/sites/api", "Microsoft.Web/sites", "api", "functionapp,linux", "azurerm_linux_function_app",
			"/subscriptions/0000/resourceGroups/web/providers/Microsoft.Web/sites/api"},
		{sub + "Microsoft.Web/sites/www", "Microsoft.Web/sites", "www", "app", "azurerm_windows_web_app",
			"/subscriptions/0000/resourceGroups/web/providers/Microsoft.Web/sites/www"},
		{sub + "microsoft.sql/servers/db1/databases/app", "Microsoft.Sql/servers/databases", "db1/app", "", "azurerm_mssql_database",
			"/subscriptions/0000/resourceGroups/web/providers/Microsoft.Sql/servers/db1/databases/app"},
	}
	for _, tt := range tests {
		r, ok, reason := MapAzure(tt.id, tt.typ, tt.name, tt.kind)
		if !ok {
			t.Errorf("%s: not mapped: %s", tt.typ, reason)
			continue
		}
		if r.Type != tt.tfType || r.ImportID != tt.wantID {
			t.Errorf("%s: got %s %q, want %s %q", tt.typ, r.Type, r.ImportID, tt.tfType, tt.wantID)
		}
	}
	if _, ok, _ := MapAzure(sub+"Microsoft.Foo/bars/x", "Microsoft.Foo/bars", "x", ""); ok {
		t.Error("an unknown type was mapped")
	}
}

func TestWriteImports(t *testing.T) {
	var b strings.Builder
	addrs, err := WriteImports(&b, "Adopted resources.\n\nRemove after applying.", []Resource{
		{Type: "aws_s3_bucket", ImportID: "site", Name: "site"},
		{Type: "aws_s3_bucket", ImportID: "Site", Name: "Site"},
		{Type: "aws_apigatewayv2_stage", ImportID: "abc/${default}", Name: "abc_$default", Source: "arn:x"},
		{Type: "aws_lambda_function", ImportID: "9lives", Name: "9lives", Note: "Check this."},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"# Adopted resources.\n#\n# Remove after applying.\n",
		"to = aws_s3_bucket.site\n",
		"to = aws_s3_bucket.site_2\n",
		`id = "abc/$${default}"`,
		"# arn:x\n",
		"to = aws_lambda_function.r_9lives\n",
		"# Check this.\nimport {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if len(addrs) != 4 {
		t.Errorf("expected 4 addresses, got %v", addrs)
	}
}

func TestPersonalIDsAreSkipped(t *testing.T) {
	res := &Result{}
	res.add(Resource{Type: "aws_sesv2_email_identity", ImportID: "someone@example.com"})
	res.add(Resource{Type: "aws_sesv2_email_identity", ImportID: "example.com"})
	res.add(Resource{Type: "aws_sesv2_email_identity", ImportID: "example.com"})
	if len(res.Resources) != 1 || res.Resources[0].ImportID != "example.com" {
		t.Fatalf("unexpected resources: %+v", res.Resources)
	}
	if len(res.Skipped) != 1 || strings.Contains(res.Skipped[0].Source, "@") {
		t.Fatalf("the email address should be skipped without being repeated: %+v", res.Skipped)
	}
}

type fakeTagging struct{ arns []string }

func (f fakeTagging) GetResources(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	out := &resourcegroupstaggingapi.GetResourcesOutput{}
	for _, a := range f.arns {
		out.ResourceTagMappingList = append(out.ResourceTagMappingList, taggingtypes.ResourceTagMapping{ResourceARN: aws.String(a)})
	}
	return out, nil
}

type fakeCloud map[string][]cctypes.ResourceDescription

func (f fakeCloud) ListResources(_ context.Context, in *cloudcontrol.ListResourcesInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error) {
	return &cloudcontrol.ListResourcesOutput{ResourceDescriptions: f[aws.ToString(in.TypeName)]}, nil
}

func TestDiscoverAWS(t *testing.T) {
	desc := func(id, props string) cctypes.ResourceDescription {
		return cctypes.ResourceDescription{Identifier: aws.String(id), Properties: aws.String(props)}
	}
	c := &AWSClients{
		Region:  "us-west-2",
		Account: "123456789012",
		Tagging: fakeTagging{arns: []string{
			"arn:aws:lambda:us-west-2:123456789012:function:app-forms",
			"arn:aws:glue:us-west-2:123456789012:job/app-etl",
			"arn:aws:ses:us-west-2:123456789012:identity/someone@example.com",
		}},
		GlobalTags: fakeTagging{arns: []string{
			"arn:aws:cloudfront::123456789012:distribution/E1",
			"arn:aws:lambda:us-east-1:123456789012:function:other-region",
		}},
		Cloud: fakeCloud{
			"AWS::Lambda::Function": {desc("app-forms", "{}"), desc("unrelated", "{}")},
			"AWS::Logs::LogGroup":   {desc("/aws/lambda/app-forms", "{}")},
		},
		GlobalCloud: fakeCloud{
			"AWS::IAM::Role":           {desc("app-forms-lambda", "{}")},
			"AWS::Route53::HostedZone": {desc("Z1", `{"Name":"app.example.org."}`)},
		},
	}
	ctx := context.Background()

	res, err := DiscoverAWS(ctx, c, AWSOptions{Tags: map[string]string{"Project": "app"}})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, r := range res.Resources {
		types[r.Type+" "+r.ImportID] = true
	}
	if !types["aws_lambda_function app-forms"] || !types["aws_cloudfront_distribution E1"] || types["aws_lambda_function other-region"] || len(res.Resources) != 2 {
		t.Errorf("unexpected tagged resources: %+v", res.Resources)
	}
	if len(res.Skipped) != 2 {
		t.Errorf("expected the glue job and the email identity to be skipped: %+v", res.Skipped)
	}

	res, err = DiscoverAWS(ctx, c, AWSOptions{Prefix: "app"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range res.Resources {
		got[r.Type+" "+r.ImportID] = true
	}
	for _, want := range []string{"aws_lambda_function app-forms", "aws_iam_role app-forms-lambda", "aws_route53_zone Z1", "aws_cloudwatch_log_group /aws/lambda/app-forms"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if got["aws_lambda_function unrelated"] {
		t.Errorf("resources outside the prefix were included: %v", got)
	}

	if _, err := DiscoverAWS(ctx, c, AWSOptions{}); err == nil {
		t.Error("expected an error without -tag or -prefix")
	}
}

type fakeAzure struct{ items []AzureResource }

func (f fakeAzure) ResourceGroup(_ context.Context, name string) (string, error) {
	return "Web-RG", nil
}
func (f fakeAzure) Resources(_ context.Context, _ string) ([]AzureResource, error) {
	return f.items, nil
}

func TestDiscoverAzure(t *testing.T) {
	l := fakeAzure{items: []AzureResource{
		{ID: "/subscriptions/s/resourceGroups/Web-RG/providers/Microsoft.Network/dnszones/example.com", Type: "Microsoft.Network/dnszones", Name: "example.com"},
		{ID: "/subscriptions/s/resourceGroups/Web-RG/providers/Microsoft.Foo/bars/x", Type: "Microsoft.Foo/bars", Name: "x"},
	}}
	res, err := DiscoverAzure(context.Background(), l, "s", "web-rg")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 2 || res.Resources[0].ImportID != "/subscriptions/s/resourceGroups/Web-RG" || res.Resources[1].Type != "azurerm_dns_zone" {
		t.Errorf("unexpected resources: %+v", res.Resources)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("expected one unsupported resource: %+v", res.Skipped)
	}
}
