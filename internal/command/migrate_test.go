// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"strings"
	"testing"

	svchost "github.com/opentofu/svchost"
)

func TestTokenEnvSuffix(t *testing.T) {
	tests := map[string]string{
		"app.terraform.io":     "app_terraform_io",
		"tfe.example-corp.com": "tfe_example__corp_com",
	}
	for host, want := range tests {
		if got := tokenEnvSuffix(svchost.Hostname(host)); got != want {
			t.Errorf("%s: got %q, want %q", host, got, want)
		}
	}
}

func TestMigrateTFC_missingArguments(t *testing.T) {
	view, done := testView(t)
	code := RunCommander(t, MigrateTFCCommander(), Meta{View: view}, []string{"-dry-run"})
	output := done(t)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d\n%s", code, output.All())
	}
	if got := output.Stderr(); !strings.Contains(got, "-organization") {
		t.Fatalf("expected help about -organization:\n%s", got)
	}
}
