// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package plugins

import (
	"context"

	"github.com/bogware/bogform/internal/addrs"
	"github.com/bogware/bogform/internal/providers"
	"github.com/bogware/bogform/internal/tfdiags"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty/function"
)

// BuildFunction implements evalglue.Providers.
func (n *newRuntimePlugins) BuildFunction(ctx context.Context, provider addrs.Provider, pf addrs.ProviderFunction, stubMissing bool, rng hcl.Range) (function.Function, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	inst, moreDiags := n.unconfiguredProviderInst(ctx, provider)
	diags = diags.Append(moreDiags)
	if moreDiags.HasErrors() {
		return function.Function{}, diags
	}

	fn, moreDiags := providers.BuildProviderFunction(ctx, inst.(providers.Interface), pf, stubMissing, rng)
	diags = diags.Append(moreDiags)
	return fn, diags

}
