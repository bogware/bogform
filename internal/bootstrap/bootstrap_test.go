// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package bootstrap

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/bogware/bogform/internal/configs"
)

func mustRepository(t *testing.T, value, platform string) Repository {
	t.Helper()
	r, err := ParseRepository(value, platform)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// checkFiles parses each file, checks it's formatted, and loads them together as a module.
func checkFiles(t *testing.T, files []File) string {
	t.Helper()
	dir := t.TempDir()
	parser := hclparse.NewParser()
	var all strings.Builder
	for _, f := range files {
		if _, diags := parser.ParseHCL(f.Content, f.Name); diags.HasErrors() {
			t.Fatalf("%s doesn't parse: %s\n%s", f.Name, diags.Error(), f.Content)
		}
		if formatted := hclwrite.Format(f.Content); !bytes.Equal(formatted, f.Content) {
			t.Errorf("%s isn't formatted", f.Name)
		}
		if strings.Contains(string(f.Content), "<no value>") || strings.Contains(string(f.Content), "{{") {
			t.Errorf("%s has unrendered template values:\n%s", f.Name, f.Content)
		}
		all.Write(f.Content)
	}
	if err := Write(dir, files, false); err != nil {
		t.Fatal(err)
	}
	if _, diags := configs.NewParser(nil).LoadConfigDir(dir); diags.HasErrors() {
		t.Fatalf("doesn't load as a module: %s", diags.Error())
	}
	return all.String()
}

func TestAWS_github(t *testing.T) {
	admin := mustRepository(t, "acme/aws-shared=repo:acme@1/aws-shared@2", PlatformGitHub)
	files, err := AWS(AWSOptions{
		Region:              "eu-west-1",
		Repositories:        []Repository{mustRepository(t, "acme/Web.Site", PlatformGitHub), admin},
		Admin:               &admin,
		PassphraseParameter: "/acme/aws-shared/state-passphrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := checkFiles(t, files)
	for _, want := range []string{
		`"web-site" = {`,
		`subject_prefix = "repo:acme/Web.Site"`,
		`subject_prefix = "repo:acme@1/aws-shared@2"`,
		`:pull_request"`,
		`:ref:refs/heads/main"`,
		`"lambda.eu-west-1.amazonaws.com"`,
		`data "aws_iam_policy_document" "aws_shared_apply"`,
		`data "aws_iam_policy_document" "web_site_apply"`,
		`REPLACE THIS`,
		`key_provider "passphrase" "state"`,
		`aws_ssm  = "/acme/aws-shared/state-passphrase"`,
		`keychain = "aws-shared"`,
		`prevent_destroy = true`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	// The admin repository's policy manages roles; it mustn't also get a placeholder.
	if strings.Count(text, `"aws_shared_apply"`) != 1 {
		t.Errorf("the admin repository should have one apply policy:\n%s", text)
	}
}

func TestAWS_gitlab(t *testing.T) {
	files, err := AWS(AWSOptions{
		Platform:     PlatformGitLab,
		GitLabURL:    "https://gitlab.example.com",
		Branch:       "trunk",
		Repositories: []Repository{mustRepository(t, "group/sub/project", PlatformGitLab)},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := checkFiles(t, files)
	for _, want := range []string{
		`url            = "https://gitlab.example.com"`,
		`client_id_list = ["https://gitlab.example.com"]`,
		`subject_prefix = "project_path:group/sub/project"`,
		`:ref_type:branch:ref:*"`,
		`:ref_type:branch:ref:trunk"`,
		`keychain = "bogform-bootstrap"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(text, "thumbprint_list") || strings.Contains(text, "aws_ssm") {
		t.Errorf("unexpected settings:\n%s", text)
	}
}

func TestAzure(t *testing.T) {
	for _, rg := range []string{"", "web-prod"} {
		files, err := Azure(AzureOptions{
			Repositories:          []Repository{mustRepository(t, "acme/web", PlatformGitHub)},
			ResourceGroup:         rg,
			PassphraseVaultSecret: "https://acme-vault.vault.azure.net/secrets/state-passphrase",
		})
		if err != nil {
			t.Fatal(err)
		}
		text := checkFiles(t, files)
		for _, want := range []string{`"web" = {`, `role_definition_name = "Reader"`, `role_definition_name = "Contributor"`, `:pull_request"`, `:ref:refs/heads/main"`, `azure_key_vault`} {
			if !strings.Contains(text, want) {
				t.Errorf("missing %q", want)
			}
		}
		if (rg != "") != strings.Contains(text, `data "azurerm_resource_group" "target"`) {
			t.Errorf("resource group %q isn't reflected:\n%s", rg, text)
		}
	}
}

func TestParseRepository(t *testing.T) {
	for value, want := range map[string]Repository{
		"acme/infra":              {Key: "infra", Name: "acme/infra", SubjectPrefix: "repo:acme/infra"},
		"acme/bogware.com":        {Key: "bogware-com", Name: "acme/bogware.com", SubjectPrefix: "repo:acme/bogware.com"},
		"acme/x=repo:acme@9/x@10": {Key: "x", Name: "acme/x", SubjectPrefix: "repo:acme@9/x@10"},
	} {
		got, err := ParseRepository(value, PlatformGitHub)
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v", value, got, err)
		}
	}
	for _, bad := range []string{"infra", "acme/infra\"", "acme/x=repo:\"evil", "acme/${x}", "/x"} {
		if _, err := ParseRepository(bad, PlatformGitHub); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
}

func TestRejectsBadOptions(t *testing.T) {
	repo := []Repository{{Key: "x", Name: "acme/x", SubjectPrefix: "repo:acme/x"}}
	for name, err := range map[string]error{
		"no repositories": func() error { _, err := AWS(AWSOptions{}); return err }(),
		"bad region":      func() error { _, err := AWS(AWSOptions{Repositories: repo, Region: "moon"}); return err }(),
		"bad platform":    func() error { _, err := AWS(AWSOptions{Repositories: repo, Platform: "svn"}); return err }(),
		"bad gitlab": func() error {
			_, err := AWS(AWSOptions{Repositories: repo, Platform: PlatformGitLab, GitLabURL: "http://x"})
			return err
		}(),
		"bad parameter": func() error {
			_, err := AWS(AWSOptions{Repositories: repo, PassphraseParameter: "no-slash"})
			return err
		}(),
		"same key": func() error {
			_, err := AWS(AWSOptions{Repositories: []Repository{repo[0], {Key: "x", Name: "other/x", SubjectPrefix: "repo:other/x"}}})
			return err
		}(),
		"bad location": func() error { _, err := Azure(AzureOptions{Repositories: repo, Location: "East US"}); return err }(),
		"bad vault": func() error {
			_, err := Azure(AzureOptions{Repositories: repo, PassphraseVaultSecret: "https://evil.example/x"})
			return err
		}(),
	} {
		if err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWrite_refusesToReplace(t *testing.T) {
	dir := t.TempDir()
	files := []File{{Name: "main.tf", Content: []byte("# new\n")}}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, files, false); err == nil {
		t.Fatal("should refuse to replace a file")
	}
	if err := Write(dir, files, true); err != nil {
		t.Fatal(err)
	}
}
