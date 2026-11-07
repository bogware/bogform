// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bogware/bogform/internal/backend"
	"github.com/bogware/bogform/internal/command/arguments"
	"github.com/bogware/bogform/internal/command/clistate"
	"github.com/bogware/bogform/internal/command/views"
	"github.com/bogware/bogform/internal/encryption/keyprovider/passphrase"
	"github.com/bogware/bogform/internal/states/statemgr"
	"github.com/bogware/bogform/internal/tfdiags"
	"github.com/bogware/bogform/internal/tofu"
)

// StateRekeyCommander builds the "state rekey" command.
func StateRekeyCommander() Command {
	cmd := Command{
		Name:  "rekey",
		Short: "Re-encrypt the state with the current encryption method",
		Long: `Reads every workspace's state and writes it again, encrypted with the "state" block's method. Use this to rotate a passphrase or key:

  1. Add the new key provider and method under new names, and make the new method the state block's method.
  2. Move the old method to a "fallback" block, keeping the old key provider's name: pbkdf2 finds its settings by that name.
  3. Run "bogform state rekey".
  4. Remove the fallback.

Earlier versions in the backend's history, such as git commits or S3 object versions, stay encrypted with the old key. If the old key was exposed, treat those versions as exposed too.`,
		DiagsWithNewline: true,
	}
	args := arguments.BindStateRekey(&cmd.CommandLine)
	cmd.Run = func(meta Meta) int {
		return StateRekeyCommand{StateMeta{meta}}.Execute(args, views.NewText(meta.View), views.NewState(args.View, meta.View))
	}
	return cmd
}

// StateRekeyCommand rewrites state with the primary encryption method.
type StateRekeyCommand struct {
	StateMeta
}

func (c StateRekeyCommand) Execute(args *arguments.StateRekey, view *views.Text, stateView views.State) int {
	ctx := c.CommandContext()
	if diags := c.Meta.checkRequiredVersion(ctx); diags != nil {
		view.Diagnostics(diags)
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

	workspaces := []string{args.Workspace}
	if args.Workspace == "" {
		all, err := b.Workspaces(ctx)
		if err != nil {
			view.Diagnostics(tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Couldn't list workspaces", err.Error())))
			return 1
		}
		workspaces = all
	}

	failed := false
	for _, workspace := range workspaces {
		result, err := c.rekeyWorkspace(ctx, b, workspace, stateView)
		if err != nil {
			failed = true
			view.Eprintf("%s: %s\n", workspace, err)
			continue
		}
		view.Printf("%s: %s\n", workspace, result)
	}
	view.Printf("\nEarlier versions in the backend's history are still encrypted with the old key. If it was exposed, treat them as exposed too.\n")
	if failed {
		return 1
	}
	return 0
}

func (c StateRekeyCommand) rekeyWorkspace(ctx context.Context, b backend.Backend, workspace string, stateView views.State) (result string, err error) {
	if diags := c.remoteVersionCheck(b, workspace); diags.HasErrors() {
		return "", diags.Err()
	}
	stateMgr, err := b.StateMgr(ctx, workspace)
	if err != nil {
		return "", fmt.Errorf("couldn't load the state: %w", err)
	}
	if c.stateArgs.Lock {
		locker := clistate.NewLocker(c.stateArgs.LockTimeout, stateView.Backend().StateLocker())
		if diags := locker.Lock(stateMgr, "state-rekey"); diags.HasErrors() {
			return "", diags.Err()
		}
		defer func() {
			if diags := locker.Unlock(); diags.HasErrors() && err == nil {
				err = fmt.Errorf("the state was re-encrypted, but is still locked: %w", diags.Err())
			}
		}()
	}
	if err := stateMgr.RefreshState(ctx); err != nil {
		return "", fmt.Errorf("couldn't read the state: %w", err)
	}
	current := statemgr.Export(stateMgr)
	if current == nil || current.State == nil || current.Lineage == "" {
		return "no state, nothing to do", nil
	}
	// A state that's unchanged isn't normally written again, so bump the serial to force the write.
	before := current.Serial
	current.Serial++
	if err := statemgr.Import(current, stateMgr, true); err != nil {
		return "", fmt.Errorf("couldn't prepare the state: %w", err)
	}
	if err := stateMgr.PersistState(ctx, nil); err != nil {
		return "", fmt.Errorf("couldn't write the state: %w", err)
	}
	after := before + 1
	if meta, ok := stateMgr.(statemgr.PersistentMeta); ok {
		after = meta.StateSnapshotMeta().Serial
	}
	return fmt.Sprintf("re-encrypted (serial %d → %d)", before, after), nil
}

// StatePassphraseCommander builds the "state passphrase" commands, which manage passphrases in the OS
// keychain for the passphrase key provider.
func StatePassphraseCommander() Command {
	return Command{
		Name:  "passphrase",
		Short: "Store state passphrases in the OS keychain",
		Long: `These commands store and remove state passphrases in the OS keychain: Windows Credential Manager, the macOS Keychain, or the Secret Service on Linux. The "passphrase" key provider reads an entry when its "keychain" attribute names it:

  key_provider "passphrase" "state" {
    env      = "BOGFORM_STATE_PASSPHRASE"
    keychain = "my-repository"
  }`,
		Commands: []Command{
			statePassphraseSetCommander(),
			statePassphraseDeleteCommander(),
		},
	}
}

func statePassphraseSetCommander() Command {
	cmd := Command{
		Name:  "set",
		Short: "Store a passphrase in the OS keychain",
		Long: fmt.Sprintf(`Stores a passphrase in the OS keychain under NAME, replacing any that's there. Bogform asks for it without showing what you type, or reads it from standard input when that isn't a terminal:

  aws ssm get-parameter --name /my/passphrase --with-decryption --query Parameter.Value --output text | bogform state passphrase set my-repository

The passphrase must have at least %d characters. Bogform never prints it.`, passphrase.MinimumLength),
		DiagsWithNewline: true,
	}
	args := arguments.BindStatePassphrase(&cmd.CommandLine)
	cmd.Run = func(meta Meta) int {
		view := views.NewText(meta.View)
		value, err := readPassphrase(meta, view, os.Stdin)
		if err != nil {
			view.Diagnostics(tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Couldn't read the passphrase", err.Error())))
			return 1
		}
		if err := passphrase.SetKeychain(args.Name, value); err != nil {
			view.Diagnostics(tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Couldn't store the passphrase", err.Error())))
			return 1
		}
		view.Printf("Stored the passphrase in the OS keychain as %q. Use keychain = %q in a passphrase key provider to read it.\n", args.Name, args.Name)
		return 0
	}
	return cmd
}

func statePassphraseDeleteCommander() Command {
	cmd := Command{
		Name:             "delete",
		Short:            "Remove a passphrase from the OS keychain",
		Long:             `Removes the passphrase stored under NAME from the OS keychain. Keep another copy first: without the passphrase, the state can't be read.`,
		DiagsWithNewline: true,
	}
	args := arguments.BindStatePassphrase(&cmd.CommandLine)
	cmd.Run = func(meta Meta) int {
		view := views.NewText(meta.View)
		existed, err := passphrase.DeleteKeychain(args.Name)
		if err != nil {
			view.Diagnostics(tfdiags.Diagnostics{}.Append(tfdiags.Sourceless(tfdiags.Error, "Couldn't remove the passphrase", err.Error())))
			return 1
		}
		if !existed {
			view.Printf("There's no passphrase named %q in the OS keychain.\n", args.Name)
			return 0
		}
		view.Printf("Removed the passphrase %q from the OS keychain.\n", args.Name)
		return 0
	}
	return cmd
}

// readPassphrase reads a passphrase from a no-echo prompt, or from standard input when it's piped.
func readPassphrase(meta Meta, view *views.Text, stdin io.Reader) (string, error) {
	if !view.StdinIsTerminal() {
		data, err := io.ReadAll(bufio.NewReader(stdin))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	ask := func(query string) (string, error) {
		return meta.UIInput().Input(context.Background(), &tofu.InputOpts{Id: "passphrase", Query: query, Secret: true})
	}
	first, err := ask("Passphrase")
	if err != nil {
		return "", err
	}
	if len(first) < passphrase.MinimumLength {
		return "", fmt.Errorf("the passphrase is too short: it must have at least %d characters", passphrase.MinimumLength)
	}
	second, err := ask("Passphrase again")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("the two passphrases don't match")
	}
	return first, nil
}
