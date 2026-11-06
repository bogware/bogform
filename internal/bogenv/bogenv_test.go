// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package bogenv

import (
	"maps"
	"testing"
)

func TestApply(t *testing.T) {
	tests := map[string]struct {
		environ []string
		want    map[string]string
	}{
		"fixed name": {
			environ: []string{"BOG_LOG=TRACE"},
			want:    map[string]string{"TF_LOG": "TRACE"},
		},
		"dynamic name keeps its case": {
			environ: []string{"BOG_VAR_instanceType=t4g.micro"},
			want:    map[string]string{"TF_VAR_instanceType": "t4g.micro"},
		},
		"value containing equals": {
			environ: []string{"BOG_CLI_ARGS_plan=-var=a=b"},
			want:    map[string]string{"TF_CLI_ARGS_plan": "-var=a=b"},
		},
		"empty value still overrides": {
			environ: []string{"BOG_LOG="},
			want:    map[string]string{"TF_LOG": ""},
		},
		"tofu only name": {
			environ: []string{"BOG_X_EXPERIMENTAL_RUNTIME=1"},
			want:    map[string]string{"TOFU_X_EXPERIMENTAL_RUNTIME": "1"},
		},
		"unrelated variables are ignored": {
			environ: []string{"TF_LOG=INFO", "PATH=/bin", "BOG_", "BOGUS=1", "=C:=C:\\"},
			want:    map[string]string{},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := map[string]string{}
			apply(test.environ, func(key, value string) error {
				got[key] = value
				return nil
			})
			if !maps.Equal(got, test.want) {
				t.Fatalf("wrong result\ngot:  %v\nwant: %v", got, test.want)
			}
		})
	}
}
