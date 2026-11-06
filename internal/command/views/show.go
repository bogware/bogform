// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package views

import (
	"context"
	"fmt"
	"os"

	"github.com/bogware/bogform/internal/command/arguments"
	"github.com/bogware/bogform/internal/command/jsonconfig"
	"github.com/bogware/bogform/internal/command/jsonformat"
	"github.com/bogware/bogform/internal/command/jsonplan"
	"github.com/bogware/bogform/internal/command/jsonprovider"
	"github.com/bogware/bogform/internal/command/jsonstate"
	"github.com/bogware/bogform/internal/configs"
	"github.com/bogware/bogform/internal/plans"
	"github.com/bogware/bogform/internal/states/statefile"
	"github.com/bogware/bogform/internal/tfdiags"
	"github.com/bogware/bogform/internal/tofu"
)

type Show interface {
	// DisplayState renders the given state snapshot, returning a status code for "tofu show" to return.
	DisplayState(ctx context.Context, stateFile *statefile.File, schemas *tofu.Schemas) int

	// DisplayPlan renders the given plan, returning a status code for "tofu show" to return.
	DisplayPlan(ctx context.Context, plan *plans.Plan, config *configs.Config, priorStateFile *statefile.File, schemas *tofu.Schemas) int

	// DisplayConfig renders the given configuration, returning a status code for "tofu show" to return.
	DisplayConfig(config *configs.Config, schemas *tofu.Schemas) int

	// DisplaySingleModule renders just one module, in a format that's a subset
	// of that used by [Show.DisplayConfig] which we can produce without
	// schema or child module information.
	DisplaySingleModule(module *configs.Module) int

	// Diagnostics renders early diagnostics, resulting from argument parsing.
	Diagnostics(diags tfdiags.Diagnostics)
}

func NewShow(args *arguments.View, view *View) Show {
	var show Show
	switch args.ViewType {
	case arguments.ViewJSON:
		show = &ShowJSON{view: view, output: view.streams.Stdout.File}
	case arguments.ViewHuman:
		show = &ShowHuman{view: view}
	default:
		panic(fmt.Sprintf("unknown view type %v", args.ViewType))
	}

	if args.JSONInto != nil {
		show = &ShowMulti{show, &ShowJSON{view: view, output: args.JSONInto}}
	}

	return show
}

type ShowMulti []Show

var _ Show = (ShowMulti)(nil)

func (m ShowMulti) DisplayState(ctx context.Context, stateFile *statefile.File, schemas *tofu.Schemas) int {
	code := 0
	for _, s := range m {
		code = max(code, s.DisplayState(ctx, stateFile, schemas))
	}
	return code
}

func (m ShowMulti) DisplayPlan(ctx context.Context, plan *plans.Plan, config *configs.Config, priorStateFile *statefile.File, schemas *tofu.Schemas) int {
	code := 0
	for _, s := range m {
		code = max(code, s.DisplayPlan(ctx, plan, config, priorStateFile, schemas))
	}
	return code
}

func (m ShowMulti) DisplayConfig(config *configs.Config, schemas *tofu.Schemas) int {
	code := 0
	for _, s := range m {
		code = max(code, s.DisplayConfig(config, schemas))
	}
	return code
}

func (m ShowMulti) DisplaySingleModule(module *configs.Module) int {
	code := 0
	for _, s := range m {
		code = max(code, s.DisplaySingleModule(module))
	}
	return code
}

func (m ShowMulti) Diagnostics(diags tfdiags.Diagnostics) {
	for _, s := range m {
		s.Diagnostics(diags)
	}
}

type ShowHuman struct {
	view *View
}

var _ Show = (*ShowHuman)(nil)

func (v *ShowHuman) DisplayState(_ context.Context, stateFile *statefile.File, schemas *tofu.Schemas) int {
	renderer := jsonformat.Renderer{
		Colorize:            v.view.colorize,
		Streams:             v.view.streams,
		RunningInAutomation: v.view.runningInAutomation,
		ShowSensitive:       v.view.showSensitive,
	}

	if stateFile == nil {
		v.view.streams.Println("No state.")
		return 0
	}

	root, outputs, err := jsonstate.MarshalForRenderer(stateFile, schemas)
	if err != nil {
		v.view.streams.Eprintf("Failed to marshal state to json: %s", err)
		return 1
	}

	jstate := jsonformat.State{
		StateFormatVersion:    jsonstate.FormatVersion,
		ProviderFormatVersion: jsonprovider.FormatVersion,
		RootModule:            root,
		RootModuleOutputs:     outputs,
		ProviderSchemas:       jsonprovider.MarshalForRenderer(schemas),
	}

	renderer.RenderHumanState(jstate)
	return 0
}

func (v *ShowHuman) DisplayPlan(_ context.Context, plan *plans.Plan, config *configs.Config, priorStateFile *statefile.File, schemas *tofu.Schemas) int {
	renderer := jsonformat.Renderer{
		Colorize:            v.view.colorize,
		Streams:             v.view.streams,
		RunningInAutomation: v.view.runningInAutomation,
		ShowSensitive:       v.view.showSensitive,
	}

	if plan != nil {
		outputs, changed, drift, attrs, err := jsonplan.MarshalForRenderer(plan, schemas)
		if err != nil {
			v.view.streams.Eprintf("Failed to marshal plan to json: %s", err)
			return 1
		}

		jplan := jsonformat.Plan{
			PlanFormatVersion:     jsonplan.FormatVersion,
			ProviderFormatVersion: jsonprovider.FormatVersion,
			OutputChanges:         outputs,
			ResourceChanges:       changed,
			ResourceDrift:         drift,
			ProviderSchemas:       jsonprovider.MarshalForRenderer(schemas),
			RelevantAttributes:    attrs,
		}

		var opts []plans.Quality
		if !plan.CanApply() {
			opts = append(opts, plans.NoChanges)
		}
		if plan.Errored {
			opts = append(opts, plans.Errored)
		}

		renderer.RenderHumanPlan(jplan, plan.UIMode, opts...)
	} else {
		v.view.streams.Println("No plan.")
	}
	return 0
}

func (v *ShowHuman) DisplayConfig(config *configs.Config, schemas *tofu.Schemas) int {
	// The human view should never be called for configuration display
	// since we require -json for -config
	v.view.streams.Eprintf("Internal error: human view should not be used for configuration display")
	return 1
}

func (v *ShowHuman) DisplaySingleModule(_ *configs.Module) int {
	// The human view should never be called for module display
	// since we require -json for -module=DIR.
	v.view.streams.Eprintf("Internal error: human view should not be used for module display")
	return 1
}

func (v *ShowHuman) Diagnostics(diags tfdiags.Diagnostics) {
	v.view.Diagnostics(diags)
}

type ShowJSON struct {
	view   *View
	output *os.File
}

var _ Show = (*ShowJSON)(nil)

func (v *ShowJSON) DisplayState(_ context.Context, stateFile *statefile.File, schemas *tofu.Schemas) int {
	jsonState, err := jsonstate.Marshal(stateFile, schemas)
	if err != nil {
		v.view.streams.Eprintf("Failed to marshal state to json: %s", err)
		return 1
	}
	fmt.Fprintln(v.output, string(jsonState))
	return 0
}

func (v *ShowJSON) DisplayPlan(_ context.Context, plan *plans.Plan, config *configs.Config, priorStateFile *statefile.File, schemas *tofu.Schemas) int {
	if plan != nil {
		planJSON, err := jsonplan.Marshal(config, plan, priorStateFile, schemas)

		if err != nil {
			v.view.streams.Eprintf("Failed to marshal plan to json: %s", err)
			return 1
		}
		fmt.Fprintln(v.output, string(planJSON))
	} else {
		// Should not get here because at least one of the two plan arguments
		// should be present, but we'll tolerate this by just returning an
		// empty JSON object.
		fmt.Fprintln(v.output, "{}")
	}
	return 0
}

func (v *ShowJSON) DisplayConfig(config *configs.Config, schemas *tofu.Schemas) int {
	configJSON, err := jsonconfig.Marshal(config, schemas)
	if err != nil {
		v.view.streams.Eprintf("Failed to marshal configuration to JSON: %s", err)
		return 1
	}
	fmt.Fprintln(v.output, string(configJSON))
	return 0
}

func (v *ShowJSON) DisplaySingleModule(module *configs.Module) int {
	moduleJSON, err := jsonconfig.MarshalSingleModule(module)
	if err != nil {
		v.view.streams.Eprintf("Failed to marshal module contents to JSON: %s", err)
		return 1
	}
	fmt.Fprintln(v.output, string(moduleJSON))
	return 0
}

// Diagnostics should only be called if show cannot be executed.
// In this case, we choose to render human-readable diagnostic output,
// primarily for backwards compatibility.
func (v *ShowJSON) Diagnostics(diags tfdiags.Diagnostics) {
	v.view.Diagnostics(diags)
}
