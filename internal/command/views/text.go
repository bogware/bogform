// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package views

import (
	"github.com/bogware/bogform/internal/tfdiags"
)

// Text is a plain view for commands whose output is a few lines of text, such as the state history
// and passphrase commands.
type Text struct {
	view *View
}

// NewText returns a Text view that writes to the view's streams.
func NewText(view *View) *Text {
	return &Text{view: view}
}

// Printf writes to standard output.
func (t *Text) Printf(format string, a ...any) {
	_, _ = t.view.streams.Printf(format, a...)
}

// Eprintf writes to standard error.
func (t *Text) Eprintf(format string, a ...any) {
	_, _ = t.view.streams.Eprintf(format, a...)
}

// Diagnostics shows diagnostics in the usual format.
func (t *Text) Diagnostics(diags tfdiags.Diagnostics) {
	t.view.Diagnostics(diags)
}

// StdinIsTerminal reports whether standard input is an interactive terminal.
func (t *Text) StdinIsTerminal() bool {
	return t.view.streams.Stdin.IsTerminal()
}
