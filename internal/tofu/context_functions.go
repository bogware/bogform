// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package tofu

import (
	"context"

	"github.com/zclconf/go-cty/cty/function"

	"github.com/bogware/bogform/internal/addrs"
	"github.com/bogware/bogform/internal/providers"
	"github.com/bogware/bogform/internal/tfdiags"
)

// This builds a provider function using an EvalContext and some additional information
// This is split out of BuiltinEvalContext for testing
func evalContextProviderFunction(ctx context.Context, provider providers.Interface, op walkOperation, pf addrs.ProviderFunction, rng tfdiags.SourceRange) (*function.Function, tfdiags.Diagnostics) {
	fn, diags := providers.BuildProviderFunction(ctx, provider, pf, op == walkValidate, rng.ToHCL())
	if diags.HasErrors() {
		return nil, diags
	}
	return &fn, diags
}
