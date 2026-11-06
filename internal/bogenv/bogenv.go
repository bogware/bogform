// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package bogenv makes BOG_* environment variables take precedence over their
// TF_* and TOFU_* equivalents.
//
// The rest of the codebase, and the providers it starts, read the older names.
// Copying the values once at startup gives every variable, including dynamic
// ones such as BOG_VAR_name, the same behavior without changing each reader.
//
// Packages that read the environment during their own initialization must import
// this package, so that its init function is guaranteed to run first.
package bogenv

import (
	"os"
	"strings"
)

const prefix = "BOG_"

// tofuOnlyNames lists the variables that exist only with a TOFU_ prefix.
var tofuOnlyNames = map[string]struct{}{
	"CPU_PROFILE":              {},
	"PANIC_TRACE":              {},
	"X_EXPERIMENTAL_RUNTIME":   {},
	"E2E_APPLY_RESOURCE_PANIC": {},
}

func init() {
	// Setenv only fails for invalid names, which can't come from the environment.
	apply(os.Environ(), os.Setenv)
}

func apply(environ []string, setenv func(key, value string) error) {
	for _, entry := range environ {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		name, isBog := strings.CutPrefix(key, prefix)
		if !isBog || name == "" {
			continue
		}
		target := "TF_" + name
		if _, tofuOnly := tofuOnlyNames[name]; tofuOnly {
			target = "TOFU_" + name
		}
		_ = setenv(target, value)
	}
}
