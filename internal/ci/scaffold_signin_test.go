// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bogware/bogform/templates"
)

const (
	testPlanRole  = "arn:aws:iam::123456789012:role/app-ci-plan"
	testApplyRole = "arn:aws:iam::123456789012:role/app-ci-apply"
	testClient    = "11111111-1111-1111-1111-111111111111"
	testPlanID    = "44444444-4444-4444-4444-444444444444"
	testTenant    = "22222222-2222-2222-2222-222222222222"
	testSub       = "33333333-3333-3333-3333-333333333333"
)

func renderValid(t *testing.T, opts ScaffoldOptions) string {
	t.Helper()
	content, err := RenderPipeline(opts)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("invalid YAML: %s\n%s", err, content)
	}
	if strings.Contains(string(content), "[[") || strings.Contains(string(content), "<no value>") {
		t.Fatalf("unrendered template values in:\n%s", content)
	}
	return string(content)
}

func TestRenderPipeline_awsSignin(t *testing.T) {
	for _, platform := range []string{PlatformGitHub, PlatformGitLab, PlatformBitbucket} {
		for _, applyOn := range []string{ApplyManual, ApplyMerge} {
			t.Run(platform+"/"+applyOn, func(t *testing.T) {
				text := renderValid(t, ScaffoldOptions{
					Platform: platform, ApplyOn: applyOn, GitState: true, Environment: "production",
					AWSPlanRole: testPlanRole, AWSApplyRole: testApplyRole, AWSRegion: "eu-west-2",
				})
				for _, want := range []string{testPlanRole, testApplyRole, "eu-west-2"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q:\n%s", want, text)
					}
				}
				if strings.Contains(text, "Sign in to your cloud provider here") {
					t.Errorf("the placeholder should be replaced:\n%s", text)
				}
				// The plan role must only appear before the apply role: plans never get apply access.
				if strings.Index(text, testPlanRole) > strings.Index(text, testApplyRole) {
					t.Errorf("the plan job should use the plan role:\n%s", text)
				}
			})
		}
	}
}

func TestRenderPipeline_azureSignin(t *testing.T) {
	for _, platform := range []string{PlatformGitHub, PlatformGitLab, PlatformBitbucket} {
		t.Run(platform, func(t *testing.T) {
			text := renderValid(t, ScaffoldOptions{
				Platform: platform, AzureClientID: testClient, AzurePlanClientID: testPlanID,
				AzureTenantID: testTenant, AzureSubscriptionID: testSub,
			})
			for _, want := range []string{testClient, testPlanID, testTenant, testSub, "ARM_USE_OIDC"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q:\n%s", want, text)
				}
			}
			if strings.Index(text, testPlanID) > strings.Index(text, testClient) {
				t.Errorf("the plan job should use the plan identity:\n%s", text)
			}
		})
	}

	t.Run(PlatformAzure, func(t *testing.T) {
		text := renderValid(t, ScaffoldOptions{Platform: PlatformAzure, GitState: true, AzureServiceConnection: "prod-connection"})
		if strings.Count(text, "azureSubscription: prod-connection") != 2 || strings.Count(text, "ARM_OIDC_TOKEN: $(ARM_OIDC_TOKEN)") != 2 {
			t.Errorf("both stages should sign in with the service connection:\n%s", text)
		}
	})
}

func TestRenderPipeline_rejectsBadSignin(t *testing.T) {
	for name, opts := range map[string]ScaffoldOptions{
		"role isn't an ARN":            {Platform: PlatformGitHub, AWSPlanRole: "app-ci-plan"},
		"role with YAML":               {Platform: PlatformGitHub, AWSApplyRole: testApplyRole + "\n  evil: true"},
		"bad region":                   {Platform: PlatformGitHub, AWSRegion: "Mars"},
		"client ID isn't a GUID":       {Platform: PlatformGitHub, AzureClientID: "abc", AzureTenantID: testTenant, AzureSubscriptionID: testSub},
		"client ID without tenant":     {Platform: PlatformGitLab, AzureClientID: testClient},
		"service connection off Azure": {Platform: PlatformGitHub, AzureServiceConnection: "prod"},
		"client ID on Azure":           {Platform: PlatformAzure, AzureClientID: testClient, AzureTenantID: testTenant, AzureSubscriptionID: testSub},
		"AWS on Azure Pipelines":       {Platform: PlatformAzure, AWSPlanRole: testPlanRole},
	} {
		if _, err := RenderPipeline(opts); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNextSteps_signin(t *testing.T) {
	without := NextSteps(PlatformGitHub, ScaffoldOptions{Platform: PlatformGitHub})
	if !strings.Contains(without, "Give the pipeline access") {
		t.Errorf("should say to set up access:\n%s", without)
	}
	both := NextSteps(PlatformGitHub, ScaffoldOptions{Platform: PlatformGitHub, AWSPlanRole: testPlanRole, AWSApplyRole: testApplyRole})
	if strings.Contains(both, "Give the pipeline access") || strings.Contains(both, "Only one") {
		t.Errorf("access is already set up:\n%s", both)
	}
	one := NextSteps(PlatformGitHub, ScaffoldOptions{Platform: PlatformGitHub, AWSPlanRole: testPlanRole})
	if !strings.Contains(one, "Only one") {
		t.Errorf("should point out the missing sign-in:\n%s", one)
	}
}

func TestRenderBogformMD(t *testing.T) {
	text, err := RenderBogformMD(ScaffoldOptions{
		Platform: PlatformGitHub, Directory: "infra", ApplyOn: ApplyMerge, GitState: true,
		AWSPlanRole: testPlanRole, AWSApplyRole: testApplyRole,
	})
	if err != nil {
		t.Fatal(err)
	}
	section := text[strings.Index(text, repositorySectionHeading):]
	for _, want := range []string{"`infra/`", "`.github/workflows/bogform.yml`", "Automatically", testPlanRole, testApplyRole, "BOGFORM_STATE_PASSPHRASE"} {
		if !strings.Contains(section, want) {
			t.Errorf("missing %q:\n%s", want, section)
		}
	}
	// Everything before the repository's section is the guide, unchanged.
	if !strings.HasPrefix(templates.BogformMD, text[:strings.Index(text, repositorySectionHeading)]) {
		t.Error("the guide itself should be copied unchanged")
	}
	if strings.Count(text, repositorySectionHeading) != 1 {
		t.Error("there should be one repository section")
	}
}

func TestWriteBogformMD(t *testing.T) {
	root := t.TempDir()
	opts := ScaffoldOptions{Platform: PlatformGitLab, Root: root}
	written, err := WriteBogformMD(opts)
	if err != nil || !written {
		t.Fatalf("written=%t err=%v", written, err)
	}
	path := filepath.Join(root, BogformMDFile)
	if err := os.WriteFile(path, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if written, err := WriteBogformMD(opts); err != nil || written {
		t.Fatalf("an edited file should be kept: written=%t err=%v", written, err)
	}
	opts.Force = true
	if written, err := WriteBogformMD(opts); err != nil || !written {
		t.Fatalf("-force should replace it: written=%t err=%v", written, err)
	}
	if content, _ := os.ReadFile(path); string(content) == "edited" {
		t.Fatal("the file wasn't replaced")
	}
}
