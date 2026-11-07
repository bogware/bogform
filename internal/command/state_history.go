// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bogware/bogform/internal/backend"
	backendLocal "github.com/bogware/bogform/internal/backend/local"
	"github.com/bogware/bogform/internal/command/arguments"
	"github.com/bogware/bogform/internal/command/clistate"
	"github.com/bogware/bogform/internal/command/views"
	"github.com/bogware/bogform/internal/states"
	"github.com/bogware/bogform/internal/states/statefile"
	"github.com/bogware/bogform/internal/states/statemgr"
	"github.com/bogware/bogform/internal/tfdiags"
	"github.com/bogware/bogform/internal/tofu"
)

// stateHistoryOf finds the history support of a backend, looking through the local backend that wraps
// remote state backends for CLI operations.
func stateHistoryOf(b backend.Backend) (backend.StateHistory, bool) {
	if h, ok := b.(backend.StateHistory); ok {
		return h, true
	}
	if l, ok := b.(*backendLocal.Local); ok && l.Backend != nil {
		h, ok := l.Backend.(backend.StateHistory)
		return h, ok
	}
	return nil, false
}

var errNoStateHistory = errors.New(`this backend doesn't keep earlier versions of state. The "git" backend keeps every version on its branch, and the "s3" backend keeps them when bucket versioning is on`)

// StateHistoryCommander builds the "state history" command.
func StateHistoryCommander() Command {
	cmd := Command{
		Name:  "history",
		Short: "List earlier versions of the state",
		Long: `Lists the stored versions of the current workspace's state, newest first.

The "git" backend keeps every version on its state branch. The "s3" backend keeps versions when bucket versioning is on. Use "bogform state restore" to go back to one of them.`,
		DiagsWithNewline: true,
	}
	args := arguments.BindStateHistory(&cmd.CommandLine)
	cmd.Run = func(meta Meta) int {
		return StateHistoryCommand{StateMeta{meta}}.Execute(args, views.NewText(meta.View))
	}
	return cmd
}

// StateHistoryCommand lists the versions of a workspace's state.
type StateHistoryCommand struct {
	StateMeta
}

func (c StateHistoryCommand) Execute(args *arguments.StateHistory, view *views.Text) int {
	ctx := c.CommandContext()
	history, workspace, diags := c.historyBackend(ctx, args.Workspace)
	if diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}

	versions, err := history.StateVersions(ctx, workspace, args.Limit)
	if err != nil {
		view.Diagnostics(diags.Append(tfdiags.Sourceless(tfdiags.Error, "Couldn't list the state's versions", err.Error())))
		return 1
	}

	if args.JSON {
		if versions == nil {
			versions = []backend.StateVersion{}
		}
		out, err := json.MarshalIndent(versions, "", "  ")
		if err != nil {
			view.Diagnostics(diags.Append(tfdiags.Sourceless(tfdiags.Error, "Couldn't encode the versions", err.Error())))
			return 1
		}
		view.Printf("%s\n", out)
		return 0
	}

	if len(versions) == 0 {
		view.Printf("The %q workspace has no stored state yet.\n", workspace)
		return 0
	}
	view.Printf("%-14s  %-19s  %9s  %s\n", "VERSION", "WRITTEN (UTC)", "SIZE", "NOTE")
	for _, v := range versions {
		note := v.Description
		if v.Current {
			note = strings.TrimSpace(note + " (current)")
		}
		view.Printf("%-14s  %-19s  %9s  %s\n", shortVersionID(v.ID), v.Time.Format("2006-01-02 15:04:05"), formatStateSize(v.Size), note)
	}
	if args.Limit > 0 && len(versions) == args.Limit {
		view.Printf("\nShowing the newest %d. Use -limit=0 to list them all.\n", args.Limit)
	}
	return 0
}

// historyBackend loads the backend and checks that it keeps state history. It also works out the
// workspace: the one given, or the selected one.
func (c *StateMeta) historyBackend(ctx context.Context, workspaceArg string) (backend.StateHistory, string, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	enc, encDiags := c.Encryption(ctx)
	diags = diags.Append(encDiags)
	if diags.HasErrors() {
		return nil, "", diags
	}
	b, backendDiags := c.Backend(ctx, nil, enc.State())
	diags = diags.Append(backendDiags)
	if diags.HasErrors() {
		return nil, "", diags
	}
	c.ignoreRemoteVersionConflict(b)

	workspace := workspaceArg
	if workspace == "" {
		selected, err := c.Workspace(ctx)
		if err != nil {
			return nil, "", diags.Append(tfdiags.Sourceless(tfdiags.Error, "Error selecting workspace", err.Error()))
		}
		workspace = selected
	}
	history, ok := stateHistoryOf(b)
	if !ok {
		return nil, "", diags.Append(tfdiags.Sourceless(tfdiags.Error, "State history isn't available", errNoStateHistory.Error()))
	}
	return history, workspace, diags
}

// shortVersionID abbreviates commit hashes. Other IDs, such as S3 version IDs, are shown in full,
// because they can't be abbreviated.
func shortVersionID(id string) string {
	if len(id) == 40 || len(id) == 64 {
		if strings.Trim(id, "0123456789abcdef") == "" {
			return id[:12]
		}
	}
	return id
}

func formatStateSize(size int64) string {
	switch {
	case size < 0:
		return "deleted"
	case size < 1024:
		return fmt.Sprintf("%d B", size)
	case size < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1024*1024))
	}
}

// StateRestoreCommander builds the "state restore" command.
func StateRestoreCommander() Command {
	cmd := Command{
		Name:  "restore",
		Short: "Make an earlier version of the state current again",
		Long: `Restores an earlier version of the current workspace's state, as "bogform state history" lists it.

The version is read and decrypted with your encryption configuration, and written as a new version with a new serial, so the version it replaces stays in the history too. Bogform shows how many resources the state has now and after restoring, and asks for confirmation unless you use -auto-approve.

Restoring changes only the state, not your infrastructure. Run "bogform plan" afterwards to see how the two differ.`,
		DiagsWithNewline: true,
	}
	args := arguments.BindStateRestore(&cmd.CommandLine)
	cmd.Run = func(meta Meta) int {
		return StateRestoreCommand{StateMeta{meta}}.Execute(args, views.NewText(meta.View), views.NewState(args.View, meta.View))
	}
	return cmd
}

// StateRestoreCommand writes an earlier version of a workspace's state as its current state.
type StateRestoreCommand struct {
	StateMeta
}

func (c StateRestoreCommand) Execute(args *arguments.StateRestore, view *views.Text, stateView views.State) int {
	ctx := c.CommandContext()
	if diags := c.Meta.checkRequiredVersion(ctx); diags != nil {
		view.Diagnostics(diags)
		return 1
	}
	var diags tfdiags.Diagnostics
	fail := func(summary string, err error) int {
		view.Diagnostics(diags.Append(tfdiags.Sourceless(tfdiags.Error, summary, err.Error())))
		return 1
	}

	enc, encDiags := c.Encryption(ctx)
	if encDiags.HasErrors() {
		view.Diagnostics(encDiags)
		return 1
	}
	b, backendDiags := c.Backend(ctx, nil, enc.State())
	if backendDiags.HasErrors() {
		view.Diagnostics(backendDiags)
		return 1
	}
	workspace := args.Workspace
	if workspace == "" {
		selected, err := c.Workspace(ctx)
		if err != nil {
			return fail("Error selecting workspace", err)
		}
		workspace = selected
	}
	if diags := c.remoteVersionCheck(b, workspace); diags.HasErrors() {
		view.Diagnostics(diags)
		return 1
	}
	history, ok := stateHistoryOf(b)
	if !ok {
		return fail("State history isn't available", errNoStateHistory)
	}

	data, err := history.StateVersionData(ctx, workspace, args.Version)
	if err != nil {
		return fail(fmt.Sprintf("Couldn't read version %q", args.Version), err)
	}
	restored, err := statefile.Read(bytes.NewReader(data), enc.State())
	if err != nil {
		return fail(fmt.Sprintf("Couldn't read version %q", args.Version), err)
	}

	stateMgr, err := b.StateMgr(ctx, workspace)
	if err != nil {
		return fail("Couldn't load the current state", err)
	}
	if c.stateArgs.Lock {
		locker := clistate.NewLocker(c.stateArgs.LockTimeout, stateView.Backend().StateLocker())
		if lockDiags := locker.Lock(stateMgr, "state-restore"); lockDiags.HasErrors() {
			view.Diagnostics(lockDiags)
			return 1
		}
		defer func() {
			if unlockDiags := locker.Unlock(); unlockDiags.HasErrors() {
				view.Diagnostics(unlockDiags)
			}
		}()
	}
	if err := stateMgr.RefreshState(ctx); err != nil {
		return fail("Couldn't load the current state", err)
	}
	current := statemgr.Export(stateMgr)
	hasCurrent := current != nil && current.State != nil && current.Lineage != ""

	if hasCurrent && restored.Lineage != current.Lineage && !args.Force {
		return fail("The version belongs to a different state",
			fmt.Errorf("its lineage is %q, but the current state's is %q. That usually means it's another configuration's state. Use -force if you're sure", restored.Lineage, current.Lineage))
	}

	before := 0
	if hasCurrent {
		before = countResourceInstances(current.State)
	}
	after := countResourceInstances(restored.State)
	view.Printf("Restoring version %s of the %q workspace's state.\n", shortVersionID(args.Version), workspace)
	view.Printf("  Resource instances now:             %d\n", before)
	view.Printf("  Resource instances after restoring: %d\n", after)
	view.Printf("This changes only the state. Run \"bogform plan\" afterwards to compare it with your infrastructure.\n")

	if !args.AutoApprove {
		ok, err := c.confirm(&tofu.InputOpts{
			Id:          "confirm",
			Query:       "\nDo you want to restore this version?",
			Description: "Only 'yes' will be accepted to continue.",
		})
		if err != nil {
			return fail("Couldn't ask for confirmation", fmt.Errorf("%w. Use -auto-approve to restore without asking", err))
		}
		if !ok {
			view.Printf("Restore cancelled.\n")
			return 0
		}
	}

	if hasCurrent && restored.Lineage == current.Lineage {
		// Writing just the state keeps the current lineage, and the state manager gives it a new serial.
		err = stateMgr.WriteState(restored.State)
	} else {
		// There's no current state, or -force: take the version's lineage too.
		err = statemgr.Import(restored, stateMgr, true)
	}
	if err != nil {
		return fail("Couldn't write the restored state", err)
	}
	if err := stateMgr.PersistState(ctx, nil); err != nil {
		return fail("Couldn't save the restored state", err)
	}
	view.Printf("\nRestored. The version it replaced is still in the history.\n")
	return 0
}

func countResourceInstances(state *states.State) int {
	if state == nil {
		return 0
	}
	count := 0
	for _, module := range state.Modules {
		for _, resource := range module.Resources {
			count += len(resource.Instances)
		}
	}
	return count
}
