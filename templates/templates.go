// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package templates holds files that people copy into their own repositories. Keeping them here,
// rather than next to the code that uses them, means the canonical copy is easy to find and link to.
package templates

import _ "embed"

// BogformMD is BOGFORM.md, the guide to a repository's Bogform setup for people and coding agents.
//
//go:embed BOGFORM.md
var BogformMD string
