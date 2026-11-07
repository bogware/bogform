// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	svchost "github.com/opentofu/svchost"

	"github.com/bogware/bogform/internal/backend"
	"github.com/bogware/bogform/internal/command/arguments"
	"github.com/bogware/bogform/internal/command/clistate"
	"github.com/bogware/bogform/internal/command/views"
	"github.com/bogware/bogform/internal/encryption"
	"github.com/bogware/bogform/internal/states/statefile"
	"github.com/bogware/bogform/internal/states/statemgr"
	"github.com/bogware/bogform/internal/tfcmigrate"
	"github.com/bogware/bogform/internal/tfdiags"
)

// MigrateCommander builds the "migrate" command, which moves infrastructure from other tools.
func MigrateCommander() Command {
	return Command{
		Name:  "migrate",
		Short: "Move state from another tool to Bogform",
		Long:  `These commands move state from other tools into the backend that this configuration uses.`,
		Commands: []Command{
			MigrateTFCCommander(),
		},
	}
}

// MigrateTFCCommander builds the "migrate tfc" command.
func MigrateTFCCommander() Command {
	cmd := Command{
		Name:  "tfc",
		Short: "Move workspaces from HCP Terraform or Terraform Enterprise",
		Long: `Copies the current state of HCP Terraform workspaces into the backend that this configuration uses, and prints what else the move needs: the workspace's settings, its variables, and the matching "bogform ci init" command.

Before running it, replace the "cloud" block or "remote" backend with the new backend, such as "git" or "s3", and configure state encryption. Then run "bogform init".

The state is written through the new backend, so it's encrypted, and it keeps its lineage and serial. Nothing in HCP Terraform is changed: delete the workspace yourself once the move is complete.

The token comes from "bogform login", or the BOG_TOKEN_<host> environment variable, such as BOG_TOKEN_app_terraform_io.`,
		DiagsWithNewline: true,
	}
	args := arguments.BindMigrateTFC(&cmd.CommandLine)
	cmd.Run = func(meta Meta) int {
		return MigrateTFCCommand{StateMeta{meta}}.Execute(args, views.NewState(args.View, meta.View), os.Stdout)
	}
	return cmd
}

// MigrateTFCCommand implements "migrate tfc".
type MigrateTFCCommand struct {
	StateMeta
}

func (c MigrateTFCCommand) Execute(args *arguments.MigrateTFC, view views.State, out io.Writer) int {
	ctx := c.CommandContext()
	fail := func(summary, detail string) int {
		view.Diagnostics(tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, summary, detail)))
		return 1
	}

	if args.Organization == "" || len(args.Workspaces) == 0 {
		return fail("Missing arguments", `Use -organization and at least one -workspace, such as: bogform migrate tfc -organization=acme -workspace=production`)
	}
	host, err := svchost.ForComparison(args.Hostname)
	if err != nil {
		return fail("Invalid hostname", err.Error())
	}
	token, err := c.tfcToken(host)
	if err != nil {
		return fail("No HCP Terraform token", err.Error())
	}
	client := tfcmigrate.NewClient(string(host), token)

	// Read everything from HCP Terraform first, so that a problem with any workspace stops the
	// move before any state is written.
	type source struct {
		workspace *tfcmigrate.Workspace
		vars      []tfcmigrate.Variable
		state     *statefile.File
	}
	var sources []source
	for _, name := range args.Workspaces {
		ws, err := client.Workspace(ctx, args.Organization, name)
		if err != nil {
			return fail(fmt.Sprintf("Couldn't read workspace %q", name), err.Error())
		}
		vars, err := client.Variables(ctx, ws.ID)
		if err != nil {
			return fail(fmt.Sprintf("Couldn't read the variables of workspace %q", name), err.Error())
		}
		src := source{workspace: ws, vars: vars}
		version, err := client.CurrentStateVersion(ctx, ws.ID)
		switch {
		case errors.Is(err, tfcmigrate.ErrNoState):
		case err != nil:
			return fail(fmt.Sprintf("Couldn't read the state of workspace %q", name), err.Error())
		default:
			raw, err := client.DownloadState(ctx, version)
			if err != nil {
				return fail(fmt.Sprintf("Couldn't download the state of workspace %q", name), err.Error())
			}
			src.state, err = statefile.Read(bytes.NewReader(raw), encryption.StateEncryptionDisabled())
			if err != nil {
				return fail(fmt.Sprintf("Couldn't read the state of workspace %q", name), err.Error())
			}
		}
		sources = append(sources, src)
	}

	var b backend.Backend
	var target string
	if !args.DryRun {
		if diags := c.Meta.checkRequiredVersion(ctx); diags != nil {
			view.Diagnostics(diags)
			return 1
		}
		enc, encDiags := c.Encryption(ctx)
		if encDiags.HasErrors() {
			view.Diagnostics(encDiags)
			return 1
		}
		var backendDiags tfdiags.Diagnostics
		b, backendDiags = c.Backend(ctx, nil, enc.State())
		if backendDiags.HasErrors() {
			view.Diagnostics(backendDiags)
			return 1
		}
		if len(sources) == 1 {
			target, err = c.Workspace(ctx)
			if err != nil {
				return fail("Error selecting workspace", err.Error())
			}
		}
	}

	for i, src := range sources {
		if i > 0 {
			fmt.Fprintln(out)
		}
		ws := src.workspace
		dest := target
		if len(sources) > 1 {
			dest = ws.Name
		}
		switch {
		case src.state == nil:
			fmt.Fprintf(out, "Workspace %q has no state, so there's nothing to copy.\n\n", ws.Name)
		case args.DryRun:
			fmt.Fprintf(out, "Would copy the state of %q: %s.\n\n", ws.Name, describeState(src.state))
		default:
			if err := c.writeMigratedState(ctx, b, dest, src.state, args, view); err != nil {
				return fail(fmt.Sprintf("Couldn't write the state of %q to workspace %q", ws.Name, dest), err.Error())
			}
			fmt.Fprintf(out, "Copied the state of %q to workspace %q: %s.\n\n", ws.Name, dest, describeState(src.state))
		}
		tfcmigrate.WriteReport(out, ws, src.vars, args.ShowValues)
	}
	if args.DryRun {
		fmt.Fprintln(out, "\nThis was a dry run. No state was written.")
	}
	return 0
}

// tfcToken finds the API token for host, from BOG_TOKEN_<host> / TF_TOKEN_<host> or the CLI
// credentials file.
func (c MigrateTFCCommand) tfcToken(host svchost.Hostname) (string, error) {
	missing := fmt.Errorf(`run "bogform login %s", or set the BOG_TOKEN_%s environment variable`, host.ForDisplay(), tokenEnvSuffix(host))
	if c.Services == nil {
		return "", missing
	}
	creds, err := c.Services.CredentialsForHost(context.Background(), host)
	if err != nil {
		return "", fmt.Errorf("couldn't read credentials for %s: %w", host.ForDisplay(), err)
	}
	if creds == nil {
		return "", missing
	}
	if tc, ok := creds.(interface{ Token() string }); ok && tc.Token() != "" {
		return tc.Token(), nil
	}
	// Credentials from a helper only expose how they authorize a request.
	req, _ := http.NewRequest(http.MethodGet, "https://"+string(host)+"/", nil)
	creds.PrepareRequest(req)
	if token, found := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer "); found && token != "" {
		return token, nil
	}
	return "", missing
}

func tokenEnvSuffix(host svchost.Hostname) string {
	out := []rune{}
	for _, r := range host.ForDisplay() {
		switch r {
		case '.':
			out = append(out, '_')
		case '-':
			out = append(out, '_', '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

func describeState(f *statefile.File) string {
	resources := 0
	for _, m := range f.State.Modules {
		resources += len(m.Resources)
	}
	return fmt.Sprintf("serial %d, %d resources, written by Terraform %s", f.Serial, resources, f.TerraformVersion)
}

// writeMigratedState writes state into a workspace of the new backend, keeping its lineage and
// serial. State that's already there is only replaced with -force.
func (c MigrateTFCCommand) writeMigratedState(ctx context.Context, b backend.Backend, workspace string, file *statefile.File, args *arguments.MigrateTFC, view views.State) error {
	mgr, err := b.StateMgr(ctx, workspace)
	if err != nil {
		return err
	}
	if c.stateArgs.Lock {
		locker := clistate.NewLocker(c.stateArgs.LockTimeout, view.Backend().StateLocker())
		if diags := locker.Lock(mgr, "migrate-tfc"); diags.HasErrors() {
			return diags.Err()
		}
		defer func() {
			if diags := locker.Unlock(); diags.HasErrors() {
				view.Diagnostics(diags)
			}
		}()
	}
	if err := mgr.RefreshState(ctx); err != nil {
		return err
	}
	if existing := mgr.State(); existing != nil && !existing.Empty() && !args.Force {
		return errors.New("the new backend already has state for this workspace. Use -force to replace it")
	}
	// Any existing state is empty, or being replaced, so its lineage doesn't matter.
	if err := statemgr.Import(file, mgr, true); err != nil {
		return err
	}
	return mgr.PersistState(ctx, nil)
}
