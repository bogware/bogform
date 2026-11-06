// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package views

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"

	"github.com/opentofu/opentofu/internal/addrs"
	"github.com/opentofu/opentofu/internal/command/arguments"
	"github.com/opentofu/opentofu/internal/configs/configschema"
	"github.com/opentofu/opentofu/internal/initwd"
	"github.com/opentofu/opentofu/internal/plans"
	"github.com/opentofu/opentofu/internal/providers"
	"github.com/opentofu/opentofu/internal/states"
	"github.com/opentofu/opentofu/internal/states/statefile"
	"github.com/opentofu/opentofu/internal/terminal"
	"github.com/opentofu/opentofu/internal/tofu"
)

func TestShowHuman_DisplayPlan(t *testing.T) {
	testCases := map[string]struct {
		plan       *plans.Plan
		schemas    *tofu.Schemas
		wantExact  bool
		wantString string
	}{
		"plan file": {
			testPlan(t),
			testSchemas(),
			false,
			"# test_resource.foo will be created",
		},
		"nothing": {
			nil,
			nil,
			true,
			"No plan.\n",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, done := terminal.StreamsForTesting(t)
			view := NewView(streams)
			view.Configure(&arguments.View{NoColor: true})
			v := NewShow(&arguments.View{ViewType: arguments.ViewHuman}, view)

			code := v.DisplayPlan(t.Context(), testCase.plan, nil, nil, testCase.schemas)
			if code != 0 {
				t.Errorf("expected 0 return code, got %d", code)
			}

			output := done(t)
			got := output.Stdout()
			want := testCase.wantString
			if (testCase.wantExact && got != want) || (!testCase.wantExact && !strings.Contains(got, want)) {
				t.Fatalf("unexpected output\ngot: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestShowHuman_DisplayState(t *testing.T) {
	testCases := map[string]struct {
		stateFile  *statefile.File
		schemas    *tofu.Schemas
		wantExact  bool
		wantString string
	}{
		"non-empty statefile": {
			&statefile.File{
				Serial:  0,
				Lineage: "fake-for-testing",
				State:   testState(),
			},
			testSchemas(),
			false,
			"# test_resource.foo:",
		},
		"empty statefile": {
			&statefile.File{
				Serial:  0,
				Lineage: "fake-for-testing",
				State:   states.NewState(),
			},
			testSchemas(),
			true,
			"The state file is empty. No resources are represented.\n",
		},
		"nothing": {
			nil,
			nil,
			true,
			"No state.\n",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, done := terminal.StreamsForTesting(t)
			view := NewView(streams)
			view.Configure(&arguments.View{NoColor: true})
			v := NewShow(&arguments.View{ViewType: arguments.ViewHuman}, view)

			code := v.DisplayState(t.Context(), testCase.stateFile, testCase.schemas)
			if code != 0 {
				t.Errorf("expected 0 return code, got %d", code)
			}

			output := done(t)
			got := output.Stdout()
			want := testCase.wantString
			if (testCase.wantExact && got != want) || (!testCase.wantExact && !strings.Contains(got, want)) {
				t.Fatalf("unexpected output\ngot: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestShowJSON_DisplayPlan(t *testing.T) {
	testCases := map[string]struct {
		plan      *plans.Plan
		stateFile *statefile.File
	}{
		"plan file": {
			testPlan(t),
			nil,
		},
		"statefile": {
			nil,
			&statefile.File{
				Serial:  0,
				Lineage: "fake-for-testing",
				State:   testState(),
			},
		},
		"empty statefile": {
			nil,
			&statefile.File{
				Serial:  0,
				Lineage: "fake-for-testing",
				State:   states.NewState(),
			},
		},
		"nothing": {
			nil,
			nil,
		},
	}

	config, _ := initwd.MustLoadConfigForTests(t, "./testdata/show", "tests")

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, done := terminal.StreamsForTesting(t)
			view := NewView(streams)
			view.Configure(&arguments.View{NoColor: true})
			v := NewShow(&arguments.View{ViewType: arguments.ViewJSON}, view)

			schemas := &tofu.Schemas{
				Providers: map[addrs.Provider]providers.ProviderSchema{
					addrs.NewDefaultProvider("test"): {
						ResourceTypes: map[string]providers.Schema{
							"test_resource": {
								Block: &configschema.Block{
									Attributes: map[string]*configschema.Attribute{
										"id":  {Type: cty.String, Optional: true, Computed: true},
										"foo": {Type: cty.String, Optional: true},
									},
								},
							},
						},
					},
				},
			}

			code := v.DisplayPlan(t.Context(), testCase.plan, config, testCase.stateFile, schemas)

			if code != 0 {
				t.Errorf("expected 0 return code, got %d", code)
			}

			// Make sure the result looks like JSON; we comprehensively test
			// the structure of this output in the command package tests.
			var result map[string]any
			got := done(t).All()
			t.Logf("output: %s", got)
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShowJSON_DisplayState(t *testing.T) {
	testCases := map[string]struct {
		stateFile *statefile.File
	}{
		"non-empty statefile": {
			&statefile.File{
				Serial:  0,
				Lineage: "fake-for-testing",
				State:   testState(),
			},
		},
		"empty statefile": {
			&statefile.File{
				Serial:  0,
				Lineage: "fake-for-testing",
				State:   states.NewState(),
			},
		},
		"nothing": {
			nil,
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, done := terminal.StreamsForTesting(t)
			view := NewView(streams)
			view.Configure(&arguments.View{NoColor: true})
			v := NewShow(&arguments.View{ViewType: arguments.ViewJSON}, view)

			schemas := &tofu.Schemas{
				Providers: map[addrs.Provider]providers.ProviderSchema{
					addrs.NewDefaultProvider("test"): {
						ResourceTypes: map[string]providers.Schema{
							"test_resource": {
								Block: &configschema.Block{
									Attributes: map[string]*configschema.Attribute{
										"id":  {Type: cty.String, Optional: true, Computed: true},
										"foo": {Type: cty.String, Optional: true},
									},
								},
							},
						},
					},
				},
			}

			code := v.DisplayState(t.Context(), testCase.stateFile, schemas)

			if code != 0 {
				t.Errorf("expected 0 return code, got %d", code)
			}

			// Make sure the result looks like JSON; we comprehensively test
			// the structure of this output in the command package tests.
			var result map[string]any
			got := done(t).All()
			t.Logf("output: %s", got)
			if err := json.Unmarshal([]byte(got), &result); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// testState returns a test State structure.
func testState() *states.State {
	return states.BuildState(func(s *states.SyncState) {
		s.SetResourceInstanceCurrent(
			addrs.Resource{
				Mode: addrs.ManagedResourceMode,
				Type: "test_resource",
				Name: "foo",
			}.Instance(addrs.NoKey).Absolute(addrs.RootModuleInstance),
			&states.ResourceInstanceObjectSrc{
				AttrsJSON: []byte(`{"id":"bar","foo":"value"}`),
				Status:    states.ObjectReady,
			},
			addrs.AbsProviderConfig{
				Provider: addrs.NewDefaultProvider("test"),
				Module:   addrs.RootModule,
			},
			addrs.NoKey,
		)
		// DeepCopy is used here to ensure our synthetic state matches exactly
		// with a state that will have been copied during the command
		// operation, and all fields have been copied correctly.
	}).DeepCopy()
}
