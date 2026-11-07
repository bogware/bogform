// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package arguments

// MigrateTFC represents the command-line arguments for the "migrate tfc" command.
type MigrateTFC struct {
	// Organization is the HCP Terraform organization.
	Organization string
	// Workspaces are the HCP Terraform workspaces to move.
	Workspaces []string
	// Hostname is the HCP Terraform or Terraform Enterprise host.
	Hostname string
	// DryRun reads everything but writes no state.
	DryRun bool
	// Force overwrites state that already exists in the new backend.
	Force bool
	// ShowValues prints the values of variables that aren't sensitive.
	ShowValues bool

	View    *View
	Vars    *Vars
	Backend *Backend
	State   *State
}

// BindMigrateTFC registers the "migrate tfc" command's arguments.
func BindMigrateTFC(cli *CommandLine) *MigrateTFC {
	ret := MigrateTFC{
		View:    BindView(cli, viewFlagNone),
		Vars:    BindVars(cli),
		Backend: BindBackend(cli),
		State:   BindState(cli, stateFlagLock),
	}
	cli.StringVar(&ret.Organization, "organization", "", "The HCP Terraform organization. Required.").SetDisplay("=NAME")
	cli.StringArrayVar(&ret.Workspaces, "workspace", nil, "An HCP Terraform workspace to move. Required. With one, its state goes to the currently selected workspace. With more than one, each goes to the workspace with the same name, which is created if it doesn't exist.").SetDisplay("=NAME")
	cli.StringVar(&ret.Hostname, "hostname", "app.terraform.io", "The host of HCP Terraform or Terraform Enterprise.").SetDisplay("=HOST")
	cli.BoolVar(&ret.DryRun, "dry-run", false, "Read the workspaces and their state, and print the report, without writing any state.")
	cli.BoolVar(&ret.Force, "force", false, "Overwrite state that already exists in the new backend.")
	cli.BoolVar(&ret.ShowValues, "show-values", false, "Print the values of variables that aren't sensitive. Sensitive values are never shown.")
	return &ret
}
