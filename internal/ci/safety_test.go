// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bogware/bogform/internal/ci/cost"
)

// The test binary stands in for bogform when fakeEnvName is set: plan reports changes, show prints the
// plan in fakePlanEnvName, and apply prints fakeAppliedText.
const (
	fakeEnvName     = "BOGFORM_CI_TEST_FAKE"
	fakePlanEnvName = "BOGFORM_CI_TEST_PLAN"
	fakeAppliedText = "FAKE APPLY RAN"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnvName) != "" {
		switch os.Args[1] {
		case "plan":
			os.Exit(2)
		case "show":
			fmt.Print(os.Getenv(fakePlanEnvName))
		case "apply":
			fmt.Println(fakeAppliedText)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeRunner(t *testing.T, plan string, opts Options, env ...string) (*Runner, *strings.Builder, *strings.Builder) {
	t.Helper()
	t.Chdir(t.TempDir())
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	opts.Binary = binary
	opts.Env = append([]string{fakeEnvName + "=1", fakePlanEnvName + "=" + plan, "PATH=" + os.Getenv("PATH")}, env...)
	opts.Stdout, opts.Stderr = &stdout, &stderr
	opts.NoComment = true
	return NewRunner(opts), &stdout, &stderr
}

func readReport(t *testing.T) Report {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(WorkDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

const destroyPlan = `{"resource_changes":[
  {"address":"aws_s3_bucket.old","mode":"managed","type":"aws_s3_bucket","provider_name":"registry.opentofu.org/hashicorp/aws","change":{"actions":["delete"],"before":{},"after":null}},
  {"address":"aws_instance.web","mode":"managed","type":"aws_instance","provider_name":"registry.opentofu.org/hashicorp/aws","change":{"actions":["delete","create"],"before":{},"after":{}}},
  {"address":"aws_iam_role.ok","mode":"managed","type":"aws_iam_role","provider_name":"registry.opentofu.org/hashicorp/aws","change":{"actions":["create"],"before":null,"after":{}}}
]}`

func TestApply_refusesDestroyByDefault(t *testing.T) {
	runner, stdout, stderr := fakeRunner(t, destroyPlan, Options{})
	if code := runner.Apply(context.Background()); code != ExitFailed {
		t.Fatalf("wrong exit code %d", code)
	}
	if strings.Contains(stdout.String(), fakeAppliedText) {
		t.Fatal("apply ran")
	}
	for _, want := range []string{"Refusing to apply", "destroys or replaces 2 resources", "aws_s3_bucket.old", "aws_instance.web", "-allow-destroy"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("missing %q in: %s", want, stderr.String())
		}
	}
}

func TestApply_allowsDestroy(t *testing.T) {
	tests := map[string]struct {
		opts Options
		env  []string
		want string
	}{
		"option":   {Options{AllowDestroy: true}, nil, "-allow-destroy"},
		"variable": {Options{}, []string{"BOG_CI_ALLOW_DESTROY=True"}, "BOG_CI_ALLOW_DESTROY"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			runner, stdout, stderr := fakeRunner(t, destroyPlan, test.opts, test.env...)
			if code := runner.Apply(context.Background()); code != ExitOK {
				t.Fatalf("wrong exit code %d: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), fakeAppliedText) {
				t.Fatal("apply didn't run")
			}
			if report := readReport(t); !strings.Contains(report.DestroyAllowedBy, test.want) {
				t.Fatalf("wrong reason: %q", report.DestroyAllowedBy)
			}
		})
	}
}

func TestApply_githubLabel(t *testing.T) {
	tests := map[string]struct {
		pulls   string
		applied bool
	}{
		"merged with label":       {`[{"number":7,"merged_at":"2026-01-01T00:00:00Z","labels":[{"name":"allow-destroy"}]}]`, true},
		"merged without label":    {`[{"number":7,"merged_at":"2026-01-01T00:00:00Z","labels":[{"name":"bug"}]}]`, false},
		"open pull request label": {`[{"number":8,"merged_at":null,"labels":[{"name":"allow-destroy"}]}]`, false},
		"no pull request":         {`[]`, false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var gotAuth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/o/r/commits/abc123/pulls" {
					http.NotFound(w, r)
					return
				}
				gotAuth = r.Header.Get("Authorization")
				io.WriteString(w, test.pulls)
			}))
			defer server.Close()
			runner, stdout, stderr := fakeRunner(t, destroyPlan, Options{},
				"GITHUB_ACTIONS=true", "GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_REPOSITORY=o/r",
				"GITHUB_SHA=abc123", "GITHUB_API_URL="+server.URL, "BOG_CI_TOKEN=token")
			code := runner.Apply(context.Background())
			if applied := strings.Contains(stdout.String(), fakeAppliedText); applied != test.applied {
				t.Fatalf("applied = %v, exit %d: %s", applied, code, stderr.String())
			}
			if gotAuth != "Bearer token" {
				t.Fatalf("wrong authorization: %q", gotAuth)
			}
			if test.applied && !strings.Contains(readReport(t).DestroyAllowedBy, "pull request #7") {
				t.Fatalf("wrong reason: %q", readReport(t).DestroyAllowedBy)
			}
			if !test.applied && !strings.Contains(stderr.String(), "`allow-destroy` label") {
				t.Fatalf("the refusal should explain the label: %s", stderr.String())
			}
		})
	}
}

func TestApply_githubLabelLookupFailureRefuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	runner, stdout, stderr := fakeRunner(t, destroyPlan, Options{},
		"GITHUB_ACTIONS=true", "GITHUB_EVENT_NAME=push", "GITHUB_REPOSITORY=o/r",
		"GITHUB_SHA=abc123", "GITHUB_API_URL="+server.URL, "BOG_CI_TOKEN=token")
	if code := runner.Apply(context.Background()); code != ExitFailed || strings.Contains(stdout.String(), fakeAppliedText) {
		t.Fatalf("should refuse, exit %d", code)
	}
	if !strings.Contains(stderr.String(), "Couldn't check pull request labels") {
		t.Fatalf("should say why: %s", stderr.String())
	}
}

func TestApply_noDestroyNeedsNoApproval(t *testing.T) {
	plan := `{"resource_changes":[{"address":"aws_iam_role.ok","mode":"managed","type":"aws_iam_role","change":{"actions":["create"],"after":{}}}]}`
	runner, stdout, stderr := fakeRunner(t, plan, Options{})
	if code := runner.Apply(context.Background()); code != ExitOK || !strings.Contains(stdout.String(), fakeAppliedText) {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
}

func TestPlan_reportsChecks(t *testing.T) {
	plan := `{"resource_changes":[
	  {"address":"aws_ebs_volume.data","mode":"managed","type":"aws_ebs_volume","provider_name":"registry.opentofu.org/hashicorp/aws","change":{"actions":["create"],"after":{"encrypted":false,"secret":"hunter2"}}}
	]}`
	runner, _, stderr := fakeRunner(t, plan, Options{})
	if code := runner.Plan(context.Background()); code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	report := readReport(t)
	if len(report.Checks) != 1 || report.Checks[0].Check != CheckEBSUnencrypted {
		t.Fatalf("checks: %+v", report.Checks)
	}
	raw, _ := os.ReadFile(filepath.Join(WorkDir, "report.json"))
	if strings.Contains(string(raw), "hunter2") {
		t.Fatal("the report must not include attribute values")
	}

	runner, _, _ = fakeRunner(t, plan, Options{NoChecks: true})
	runner.Plan(context.Background())
	if report := readReport(t); len(report.Checks) != 0 {
		t.Fatalf("checks should be off: %+v", report.Checks)
	}
}

func change(resourceType, actions string, after map[string]any) plannedResource {
	return plannedResource{Address: resourceType + ".x", Type: resourceType, Actions: strings.Split(actions, ","), After: after}
}

func TestRunChecks(t *testing.T) {
	allow := func(statement string) string {
		return `{"Version":"2012-10-17","Statement":` + statement + `}`
	}
	tests := map[string]struct {
		resource plannedResource
		want     string // The check ID expected, or "" for none.
	}{
		"public access block off":     {change("aws_s3_bucket_public_access_block", "create", map[string]any{"block_public_acls": false, "block_public_policy": true}), CheckS3PublicAccessBlock},
		"public access block on":      {change("aws_s3_bucket_public_access_block", "create", map[string]any{"block_public_acls": true, "block_public_policy": true}), ""},
		"public access block unknown": {change("aws_s3_bucket_public_access_block", "create", map[string]any{}), ""},
		"public ACL":                  {change("aws_s3_bucket_acl", "create", map[string]any{"acl": "public-read"}), CheckS3PublicACL},
		"private ACL":                 {change("aws_s3_bucket_acl", "create", map[string]any{"acl": "private"}), ""},
		"public policy":               {change("aws_s3_bucket_policy", "update", map[string]any{"policy": allow(`[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"*"}]`)}), CheckS3PublicPolicy},
		"public policy, AWS star":     {change("aws_s3_bucket_policy", "create", map[string]any{"policy": allow(`{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":"s3:GetObject","Resource":"*"}`)}), CheckS3PublicPolicy},
		"conditional public policy":   {change("aws_s3_bucket_policy", "create", map[string]any{"policy": allow(`[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"*","Condition":{"StringEquals":{"aws:SourceArn":"x"}}}]`)}), ""},
		"service principal policy":    {change("aws_s3_bucket_policy", "create", map[string]any{"policy": allow(`[{"Effect":"Allow","Principal":{"Service":"cloudfront.amazonaws.com"},"Action":"s3:GetObject","Resource":"*"}]`)}), ""},
		"unknown policy":              {change("aws_s3_bucket_policy", "create", map[string]any{"policy": nil}), ""},
		"unencrypted volume":          {change("aws_ebs_volume", "create", map[string]any{"encrypted": false}), CheckEBSUnencrypted},
		"volume encryption unknown":   {change("aws_ebs_volume", "create", map[string]any{}), ""},
		"unencrypted root device":     {change("aws_instance", "create", map[string]any{"root_block_device": []any{map[string]any{"encrypted": false}}}), CheckEBSUnencrypted},
		"unencrypted database":        {change("aws_db_instance", "create", map[string]any{"storage_encrypted": false}), CheckRDSUnencrypted},
		"encrypted database":          {change("aws_db_instance", "create", map[string]any{"storage_encrypted": true}), ""},
		"unencrypted file system":     {change("aws_efs_file_system", "create", map[string]any{"encrypted": false}), CheckEFSUnencrypted},
		"wildcard policy":             {change("aws_iam_policy", "create", map[string]any{"policy": allow(`[{"Effect":"Allow","Action":"*","Resource":"*"}]`)}), CheckIAMWildcard},
		"wildcard in list":            {change("aws_iam_role_policy", "create", map[string]any{"policy": allow(`[{"Effect":"Allow","Action":["s3:*","*"],"Resource":["*"]}]`)}), CheckIAMWildcard},
		"scoped policy":               {change("aws_iam_policy", "create", map[string]any{"policy": allow(`[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]`)}), ""},
		"wildcard deny":               {change("aws_iam_policy", "create", map[string]any{"policy": allow(`[{"Effect":"Deny","Action":"*","Resource":"*"}]`)}), ""},
		"wildcard inline policy":      {change("aws_iam_role", "create", map[string]any{"inline_policy": []any{map[string]any{"policy": allow(`{"Effect":"Allow","Action":"*","Resource":"*"}`)}}}), CheckIAMWildcard},
		"SSH open":                    {change("aws_security_group", "create", map[string]any{"ingress": []any{map[string]any{"protocol": "tcp", "from_port": 22.0, "to_port": 22.0, "cidr_blocks": []any{"0.0.0.0/0"}}}}), CheckSecurityGroupAdmin},
		"RDP in a range":              {change("aws_security_group_rule", "create", map[string]any{"type": "ingress", "protocol": "tcp", "from_port": 3000.0, "to_port": 4000.0, "ipv6_cidr_blocks": []any{"::/0"}}), CheckSecurityGroupAdmin},
		"all protocols":               {change("aws_vpc_security_group_ingress_rule", "create", map[string]any{"ip_protocol": "-1", "cidr_ipv4": "0.0.0.0/0"}), CheckSecurityGroupAdmin},
		"HTTPS open":                  {change("aws_security_group", "create", map[string]any{"ingress": []any{map[string]any{"protocol": "tcp", "from_port": 443.0, "to_port": 443.0, "cidr_blocks": []any{"0.0.0.0/0"}}}}), ""},
		"SSH from office":             {change("aws_security_group", "create", map[string]any{"ingress": []any{map[string]any{"protocol": "tcp", "from_port": 22.0, "to_port": 22.0, "cidr_blocks": []any{"203.0.113.0/24"}}}}), ""},
		"egress rule":                 {change("aws_security_group_rule", "create", map[string]any{"type": "egress", "protocol": "-1", "cidr_blocks": []any{"0.0.0.0/0"}}), ""},
		"NSG SSH":                     {change("azurerm_network_security_rule", "create", map[string]any{"direction": "Inbound", "access": "Allow", "protocol": "Tcp", "source_address_prefix": "Internet", "destination_port_range": "22"}), CheckNSGAdmin},
		"NSG any port":                {change("azurerm_network_security_group", "create", map[string]any{"security_rule": []any{map[string]any{"direction": "Inbound", "access": "Allow", "protocol": "*", "source_address_prefix": "*", "destination_port_range": "*"}}}), CheckNSGAdmin},
		"NSG RDP range list":          {change("azurerm_network_security_rule", "create", map[string]any{"direction": "Inbound", "access": "Allow", "protocol": "Tcp", "source_address_prefixes": []any{"10.0.0.0/8", "0.0.0.0/0"}, "destination_port_ranges": []any{"3380-3390"}}), CheckNSGAdmin},
		"NSG deny":                    {change("azurerm_network_security_rule", "create", map[string]any{"direction": "Inbound", "access": "Deny", "protocol": "Tcp", "source_address_prefix": "*", "destination_port_range": "22"}), ""},
		"NSG from VNet":               {change("azurerm_network_security_rule", "create", map[string]any{"direction": "Inbound", "access": "Allow", "protocol": "Tcp", "source_address_prefix": "VirtualNetwork", "destination_port_range": "22"}), ""},
		"public blob access":          {change("azurerm_storage_account", "create", map[string]any{"allow_nested_items_to_be_public": true}), CheckStoragePublicAccess},
		"HTTP allowed":                {change("azurerm_storage_account", "create", map[string]any{"https_traffic_only_enabled": false}), CheckStorageHTTPSOnly},
		"old TLS":                     {change("azurerm_storage_account", "update", map[string]any{"min_tls_version": "TLS1_0"}), CheckStorageMinTLS},
		"locked down storage":         {change("azurerm_storage_account", "create", map[string]any{"allow_nested_items_to_be_public": false, "https_traffic_only_enabled": true, "min_tls_version": "TLS1_2"}), ""},
		"public container":            {change("azurerm_storage_container", "create", map[string]any{"container_access_type": "blob"}), CheckStorageContainer},
		"open database firewall":      {change("azurerm_postgresql_flexible_server_firewall_rule", "create", map[string]any{"start_ip_address": "0.0.0.0", "end_ip_address": "255.255.255.255"}), CheckDatabaseFirewallOpen},
		"Azure services rule":         {change("azurerm_mssql_firewall_rule", "create", map[string]any{"start_ip_address": "0.0.0.0", "end_ip_address": "0.0.0.0"}), ""},
		"unchanged resource":          {change("aws_ebs_volume", "no-op", map[string]any{"encrypted": false}), ""},
		"destroyed resource":          {plannedResource{Address: "aws_ebs_volume.x", Type: "aws_ebs_volume", Actions: []string{"delete"}}, ""},
		"skipped by tag":              {change("aws_ebs_volume", "create", map[string]any{"encrypted": false, "tags": map[string]any{SkipChecksTag: "other, " + CheckEBSUnencrypted}}), ""},
		"skipped by tag, all":         {change("aws_ebs_volume", "create", map[string]any{"encrypted": false, "tags": map[string]any{SkipChecksTag: "all"}}), ""},
		"other tag doesn't skip":      {change("aws_ebs_volume", "create", map[string]any{"encrypted": false, "tags": map[string]any{SkipChecksTag: CheckRDSUnencrypted}}), CheckEBSUnencrypted},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			findings := runChecks([]plannedResource{test.resource}, nil)
			switch {
			case test.want == "" && len(findings) > 0:
				t.Fatalf("unexpected findings: %+v", findings)
			case test.want != "" && !slices.ContainsFunc(findings, func(f Finding) bool { return f.Check == test.want }):
				t.Fatalf("expected %s, got %+v", test.want, findings)
			}
		})
	}
	if !slices.Contains(CheckIDs, CheckDatabaseFirewallOpen) || len(CheckIDs) != 14 {
		t.Fatalf("CheckIDs is incomplete: %v", CheckIDs)
	}
	volume := change("aws_ebs_volume", "create", map[string]any{"encrypted": false})
	if findings := runChecks([]plannedResource{volume}, []string{CheckEBSUnencrypted}); len(findings) != 0 {
		t.Fatalf("skipped check reported: %+v", findings)
	}
}

func TestRender_safetySections(t *testing.T) {
	report := &Report{
		Kind: KindPlan, Status: StatusChanges, Directory: ".", Workspace: "default",
		Run:    Context{Platform: PlatformGitHub},
		Counts: Counts{Destroy: 1, Replace: 1},
		Changes: []Change{
			{Address: "aws_s3_bucket.old", Actions: []string{"delete"}},
			{Address: "aws_instance.web", Actions: []string{"create", "delete"}},
			{Address: "aws_iam_role.new", Actions: []string{"create"}},
		},
		Checks: []Finding{{Check: CheckEBSUnencrypted, Address: "aws_ebs_volume.data", Message: "The volume | isn't encrypted."}},
		Cost: &cost.Estimate{
			Before: 10, After: 25.5,
			Resources: []cost.Line{{Address: "aws_instance.web", Description: "t3.small (Linux)", Before: 10, After: 25.5}},
			NotPriced: []cost.NotPriced{{Address: "aws_lambda_function.f", Reason: "Usage-based: requests."}},
		},
	}
	got := report.Render(RenderOptions{Collapsible: true})
	for _, want := range []string{
		"This plan destroys or replaces 2 resources", "`aws_s3_bucket.old`", "`allow-destroy` label",
		"1 check warning", "`aws-ebs-unencrypted`", "The volume \\| isn't encrypted.",
		"Estimated monthly cost: $10.00 → $25.50 (+$15.50)", "t3.small (Linux)", "1 changed resource isn't in the estimate", "<details>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "destroys or replaces") > strings.Index(got, "| Action | Resource |") {
		t.Errorf("the destroy warning should come before the list of changes:\n%s", got)
	}

	report.Kind, report.Status, report.DestroyAllowedBy = KindApply, StatusApplied, "the -allow-destroy option"
	report.Cost = nil
	got = report.Render(RenderOptions{})
	if !strings.Contains(got, "This apply destroyed or replaced 2 resources") || !strings.Contains(got, "Allowed by the -allow-destroy option.") {
		t.Errorf("apply rendering:\n%s", got)
	}
}

func TestRenderPipeline_safetyOptions(t *testing.T) {
	wants := map[string][]string{
		PlatformGitHub:    {"allow_destroy:", "type: boolean", "inputs.allow_destroy && '-allow-destroy'", "pull-requests: read", "args: -cost"},
		PlatformGitLab:    {"BOG_CI_ALLOW_DESTROY:", "ci plan -cost"},
		PlatformAzure:     {"name: allowDestroy", "BOG_CI_ALLOW_DESTROY: ${{ parameters.allowDestroy }}", "ci plan -cost"},
		PlatformBitbucket: {"name: BOG_CI_ALLOW_DESTROY", "ci plan -cost"},
	}
	for platform, want := range wants {
		for _, applyOn := range []string{ApplyManual, ApplyMerge} {
			t.Run(platform+"/"+applyOn, func(t *testing.T) {
				content, err := RenderPipeline(ScaffoldOptions{Platform: platform, Version: "1.2.3", ApplyOn: applyOn, Cost: true, GitState: true, Environment: "production"})
				if err != nil {
					t.Fatal(err)
				}
				var parsed map[string]any
				if err := yaml.Unmarshal(content, &parsed); err != nil {
					t.Fatalf("invalid YAML: %s\n%s", err, content)
				}
				for _, w := range want {
					if platform == PlatformBitbucket && applyOn == ApplyMerge && strings.HasPrefix(w, "name:") {
						w = "set a BOG_CI_ALLOW_DESTROY"
					}
					if !strings.Contains(string(content), w) {
						t.Errorf("missing %q in:\n%s", w, content)
					}
				}
			})
		}
		content, _ := RenderPipeline(ScaffoldOptions{Platform: platform, Version: "1.2.3"})
		if strings.Contains(string(content), "-cost") {
			t.Errorf("%s: -cost without the option:\n%s", platform, content)
		}
	}
}

func TestNextSteps_mentionsLabelAndPricing(t *testing.T) {
	steps := NextSteps(PlatformGitHub, ScaffoldOptions{Cost: true})
	for _, want := range []string{AllowDestroyLabel, "pricing:GetProducts"} {
		if !strings.Contains(steps, want) {
			t.Errorf("missing %q in:\n%s", want, steps)
		}
	}
}
