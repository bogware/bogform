// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"os"
	"strings"
	"testing"

	"github.com/opentofu/opentofu/internal/command/workdir"
)

// migrationStep is a line of the removed-backend guidance that users rely on.
const migrationStep = `state pull > terraform.tfstate`

func TestInit_removedSaaSBackends(t *testing.T) {
	configs := map[string]string{
		"cloud block": `terraform {
  cloud {
    organization = "example"
    workspaces {
      name = "example"
    }
  }
}
`,
		"remote backend": `terraform {
  backend "remote" {
    organization = "example"
    workspaces {
      name = "example"
    }
  }
}
`,
		"cloud backend": `terraform {
  backend "cloud" {}
}
`,
	}

	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			td := t.TempDir()
			t.Chdir(td)
			if err := os.WriteFile("main.tf", []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			view, done := testView(t)
			meta := Meta{
				WorkingDir:       workdir.NewDir("."),
				testingOverrides: metaOverridesForProvider(testProvider()),
				View:             view,
			}

			code := RunCommander(t, InitCommander(), meta, nil)
			output := done(t)
			if code == 0 {
				t.Fatalf("expected init to fail\n%s", output.All())
			}
			if !strings.Contains(output.Stderr(), migrationStep) {
				t.Fatalf("error doesn't explain how to migrate\n%s", output.Stderr())
			}
		})
	}
}

func TestSavedBackendUnknownError(t *testing.T) {
	for _, backendType := range []string{"cloud", "remote"} {
		t.Run(backendType, func(t *testing.T) {
			got := savedBackendUnknownError(backendType).Error()
			if !strings.Contains(got, migrationStep) {
				t.Fatalf("error doesn't explain how to migrate: %s", got)
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		got := savedBackendUnknownError("nonexistent").Error()
		if strings.Contains(got, migrationStep) {
			t.Fatalf("error shouldn't mention migration for an unknown backend: %s", got)
		}
		if !strings.Contains(got, `"nonexistent"`) {
			t.Fatalf("error doesn't name the backend: %s", got)
		}
	})
}
