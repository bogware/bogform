// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package arguments

import (
	"github.com/bogware/bogform/internal/tfdiags"
)

// StateHistory represents the command-line arguments for the 'state history' command.
type StateHistory struct {
	// Workspace overrides the selected workspace.
	Workspace string
	// Limit is the most versions to list. Zero or less lists them all.
	Limit int
	// JSON prints the versions as JSON.
	JSON bool

	View    *View
	Vars    *Vars
	Backend *Backend
}

// BindStateHistory registers the CLI arguments for 'state history'.
func BindStateHistory(cli *CommandLine) *StateHistory {
	ret := StateHistory{
		View:    BindView(cli, viewFlagNone),
		Vars:    BindVars(cli),
		Backend: BindBackend(cli),
	}
	cli.StringVar(&ret.Workspace, "workspace", "", "The workspace whose history to show. Defaults to the selected workspace.")
	cli.IntVar(&ret.Limit, "limit", 20, "The most versions to list. Use 0 to list them all.")
	cli.BoolVar(&ret.JSON, "json", false, "Print the versions as JSON.")
	return &ret
}

// StateRestore represents the command-line arguments for the 'state restore' command.
type StateRestore struct {
	// Version is the ID of the version to restore, as 'state history' lists it.
	Version string
	// Workspace overrides the selected workspace.
	Workspace string
	// AutoApprove skips the confirmation.
	AutoApprove bool
	// Force allows restoring a state with a different lineage.
	Force bool

	View    *View
	Vars    *Vars
	Backend *Backend
	State   *State
}

// BindStateRestore registers the CLI arguments for 'state restore'.
func BindStateRestore(cli *CommandLine) *StateRestore {
	ret := StateRestore{
		View:    BindView(cli, viewFlagInput),
		Vars:    BindVars(cli),
		Backend: BindBackend(cli),
		State:   BindState(cli, stateFlagLock),
	}
	cli.StringVar(&ret.Workspace, "workspace", "", "The workspace to restore. Defaults to the selected workspace.")
	cli.BoolVar(&ret.AutoApprove, "auto-approve", false, "Restore without asking for confirmation.")
	cli.BoolVar(&ret.Force, "force", false, "Restore even if the version belongs to a different state, with another lineage.")
	cli.PositionalArg(&ret.Version, "VERSION", false)
	return &ret
}

// ParseStateRestore processes CLI arguments for 'state restore'. It's used by tests.
func ParseStateRestore(args []string) (*StateRestore, func(), tfdiags.Diagnostics) {
	cli := new(CommandLine)
	ret := BindStateRestore(cli)
	closer, diags := cli.parseWithHooks("state restore", args)
	return ret, closer, diags
}

// StateRekey represents the command-line arguments for the 'state rekey' command.
type StateRekey struct {
	// Workspace limits the rekey to one workspace. Empty means all of them.
	Workspace string

	View    *View
	Vars    *Vars
	Backend *Backend
	State   *State
}

// BindStateRekey registers the CLI arguments for 'state rekey'.
func BindStateRekey(cli *CommandLine) *StateRekey {
	ret := StateRekey{
		View:    BindView(cli, viewFlagNone),
		Vars:    BindVars(cli),
		Backend: BindBackend(cli),
		State:   BindState(cli, stateFlagLock),
	}
	cli.StringVar(&ret.Workspace, "workspace", "", "Rekey only this workspace. Defaults to every workspace.")
	return &ret
}

// StatePassphrase represents the command-line arguments for 'state passphrase set' and 'delete'.
type StatePassphrase struct {
	// Name is the keychain entry's name, as the passphrase key provider's "keychain" attribute gives it.
	Name string

	View *View
}

// BindStatePassphrase registers the CLI arguments for the passphrase commands.
func BindStatePassphrase(cli *CommandLine) *StatePassphrase {
	ret := StatePassphrase{
		View: BindView(cli, viewFlagNone),
	}
	cli.PositionalArg(&ret.Name, "NAME", false)
	return &ret
}
