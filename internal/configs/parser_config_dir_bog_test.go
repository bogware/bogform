// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package configs

import (
	"maps"
	"slices"
	"testing"
)

func TestParserLoadConfigDir_bogFilesTakePrecedence(t *testing.T) {
	parser := testParser(map[string]string{
		"mod/main.tf":       `variable "main_tf" {}`,
		"mod/main.tofu":     `variable "main_tofu" {}`,
		"mod/main.bog":      `variable "main_bog" {}`,
		"mod/both.tf":       `variable "both_tf" {}`,
		"mod/both.tofu":     `variable "both_tofu" {}`,
		"mod/json.tf.json":  `{"variable": {"json_tf": {}}}`,
		"mod/json.bog.json": `{"variable": {"json_bog": {}}}`,
		"mod/alone.bog":     `variable "alone_bog" {}`,
		"mod/alone_tf.tf":   `variable "alone_tf" {}`,
		"mod/alone_to.tofu": `variable "alone_tofu" {}`,
	})

	mod, diags := parser.LoadConfigDir("mod")
	if diags.HasErrors() {
		t.Fatal(diags.Error())
	}

	got := slices.Sorted(maps.Keys(mod.Variables))
	want := []string{"alone_bog", "alone_tf", "alone_tofu", "both_tofu", "json_bog", "main_bog"}
	if !slices.Equal(got, want) {
		t.Fatalf("wrong variables\ngot:  %v\nwant: %v", got, want)
	}
}

func TestFilterSupersededPaths_tests(t *testing.T) {
	got := filterSupersededPaths([]string{"a.tftest.hcl", "a.tofutest.hcl", "a.bogtest.hcl", "b.tftest.hcl", "c.tofutest.json", "c.bogtest.json"})
	want := []string{"a.bogtest.hcl", "b.tftest.hcl", "c.bogtest.json"}
	if !slices.Equal(got, want) {
		t.Fatalf("wrong paths\ngot:  %v\nwant: %v", got, want)
	}
}

func TestFileExt_bog(t *testing.T) {
	for path, want := range map[string]string{
		"main.bog":          ".bog",
		"main.bog.json":     ".bog.json",
		"main.bogtest.hcl":  ".bogtest.hcl",
		"main.bogtest.json": ".bogtest.json",
	} {
		if got := fileExt(path); got != want {
			t.Errorf("fileExt(%q) = %q, want %q", path, got, want)
		}
	}
	if got, ok := mockFileExt("main.bogmock.hcl"); !ok || got != ".bogmock.hcl" {
		t.Errorf("mockFileExt = %q, %t", got, ok)
	}
}
