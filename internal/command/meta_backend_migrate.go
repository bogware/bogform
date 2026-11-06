// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bogware/bogform/internal/backend"
	"github.com/bogware/bogform/internal/command/clistate"
	"github.com/bogware/bogform/internal/command/views"
	"github.com/bogware/bogform/internal/encryption"
	"github.com/bogware/bogform/internal/states"
	"github.com/bogware/bogform/internal/states/statemgr"
	"github.com/bogware/bogform/internal/tofu"
)

type backendMigrateOpts struct {
	SourceType, DestinationType string
	Source, Destination         backend.Backend
	View                        views.Backend

	// Fields below are set internally when migrate is called

	sourceWorkspace      string
	destinationWorkspace string
	force                bool // if true, won't ask for confirmation
}

func (b *backendMigrateOpts) backendView(base *views.View) views.Backend {
	if b != nil && b.View != nil {
		return b.View
	}
	return views.NewBackendHuman(base)
}

// backendMigrateState handles migrating (copying) state from one backend
// to another. This function handles asking the user for confirmation
// as well as the copy itself.
//
// This function can handle all scenarios of state migration regardless
// of the existence of state in either backend.
//
// After migrating the state, the existing state in the first backend
// remains untouched.
//
// This will attempt to lock both states for the migration.
func (m *Meta) backendMigrateState(ctx context.Context, opts *backendMigrateOpts) error {
	log.Printf("[INFO] backendMigrateState: need to migrate from %q to %q backend config", opts.SourceType, opts.DestinationType)
	// We need to check what the named state status is. If we're converting
	// from multi-state to single-state for example, we need to handle that.
	var sourceSingleState, destinationSingleState bool

	sourceWorkspaces, sourceSingleState, err := retrieveWorkspaces(ctx, opts.Source, opts.SourceType)
	if err != nil {
		return err
	}
	destinationWorkspaces, destinationSingleState, err := retrieveWorkspaces(ctx, opts.Destination, opts.SourceType)
	if err != nil {
		return err
	}

	// Set up defaults
	opts.sourceWorkspace = backend.DefaultStateName
	opts.destinationWorkspace = backend.DefaultStateName
	opts.force = m.backendArgs.ForceInitCopy

	// Disregard remote OpenTofu version for the state source backend, as we are
	// migrating away and will not break a remote workspace.
	m.ignoreRemoteVersionConflict(opts.Source)

	// Disregard remote OpenTofu version if instructed to do so via CLI flag.
	if m.backendArgs.IgnoreRemoteVersion {
		m.ignoreRemoteVersionConflict(opts.Destination)
	} else {
		// Check the remote OpenTofu version for the state destination backend, to
		// ensure that we don't break the workspace by uploading an incompatible
		// state file.
		for _, workspace := range destinationWorkspaces {
			diags := m.remoteVersionCheck(opts.Destination, workspace)
			if diags.HasErrors() {
				return diags.Err()
			}
		}
		// If there are no specified destination workspaces, perform a remote
		// backend version check with the default workspace.
		if len(destinationWorkspaces) == 0 {
			diags := m.remoteVersionCheck(opts.Destination, backend.DefaultStateName)
			if diags.HasErrors() {
				return diags.Err()
			}
		}
	}

	// Determine migration behavior based on whether the source/destination
	// supports multi-state.
	switch {
	// Single-state to single-state. This is the easiest case: we just
	// copy the default state directly.
	case sourceSingleState && destinationSingleState:
		return m.backendMigrateState_s_s(ctx, opts)

	// Single-state to multi-state. This is easy since we just copy
	// the default state and ignore the rest in the destination.
	case sourceSingleState && !destinationSingleState:
		return m.backendMigrateState_s_s(ctx, opts)

	// Multi-state to single-state. If the source has more than the default
	// state this is complicated since we have to ask the user what to do.
	case !sourceSingleState && destinationSingleState:
		// If the source only has one state and it is the default,
		// treat it as if it doesn't support multi-state.
		if len(sourceWorkspaces) == 1 && sourceWorkspaces[0] == backend.DefaultStateName {
			return m.backendMigrateState_s_s(ctx, opts)
		}

		return m.backendMigrateState_S_s(ctx, opts)

	// Multi-state to multi-state. We merge the states together (migrating
	// each from the source to the destination one by one).
	case !sourceSingleState && !destinationSingleState:
		// If the source only has one state and it is the default,
		// treat it as if it doesn't support multi-state.
		if len(sourceWorkspaces) == 1 && sourceWorkspaces[0] == backend.DefaultStateName {
			return m.backendMigrateState_s_s(ctx, opts)
		}

		return m.backendMigrateState_S_S(ctx, opts)
	}

	return nil
}

// -------------------------------------------------------------------
// State Migration Scenarios
//
// The functions below cover handling all the various scenarios that
// can exist when migrating state. They are named in an immediately not
// obvious format but is simple:
//
// Format: backendMigrateState_s1_s2[_suffix]
//
// When s1 or s2 is lower case, it means that it is a single state backend.
// When either is uppercase, it means that state is a multi-state backend.
// The suffix is used to disambiguate multiple cases with the same type of
// states.
//
// -------------------------------------------------------------------

// Multi-state to multi-state.
func (m *Meta) backendMigrateState_S_S(ctx context.Context, opts *backendMigrateOpts) error {
	log.Print("[INFO] backendMigrateState: migrating all named workspaces")

	migrate := opts.force
	if !migrate {
		var err error
		// Ask the user if they want to migrate their existing remote state
		migrate, err = m.confirm(&tofu.InputOpts{
			Id: "backend-migrate-multistate-to-multistate",
			Query: fmt.Sprintf(
				"Do you want to migrate all workspaces to %q?",
				opts.DestinationType),
			Description: fmt.Sprintf(
				strings.TrimSpace(inputBackendMigrateMultiToMulti),
				opts.SourceType, opts.DestinationType),
		})
		if err != nil {
			return fmt.Errorf(
				"Error asking for state migration action: %w", err)
		}
	}
	if !migrate {
		return fmt.Errorf("Migration aborted by user.")
	}

	// Read all the states
	sourceWorkspaces, err := opts.Source.Workspaces(ctx)
	if err != nil {
		return fmt.Errorf(strings.TrimSpace(
			errMigrateLoadStates), opts.SourceType, err)
	}

	// Sort the states so they're always copied alphabetically
	sort.Strings(sourceWorkspaces)

	// Go through each and migrate
	for _, name := range sourceWorkspaces {
		// Copy the same names
		opts.sourceWorkspace = name
		opts.destinationWorkspace = name

		// Force it, we confirmed above
		opts.force = true

		// Perform the migration
		if err := m.backendMigrateState_s_s(ctx, opts); err != nil {
			return fmt.Errorf(strings.TrimSpace(
				errMigrateMulti), name, opts.SourceType, opts.DestinationType, err)
		}
	}

	return nil
}

// Multi-state to single state.
func (m *Meta) backendMigrateState_S_s(ctx context.Context, opts *backendMigrateOpts) error {
	log.Printf("[INFO] backendMigrateState: destination backend type %q does not support named workspaces", opts.DestinationType)

	currentWorkspace, err := m.Workspace(ctx)
	if err != nil {
		return err
	}

	migrate := opts.force
	if !migrate {
		var err error
		// Ask the user if they want to migrate their existing remote state
		migrate, err = m.confirm(&tofu.InputOpts{
			Id: "backend-migrate-multistate-to-single",
			Query: fmt.Sprintf(
				"Destination state %q doesn't support workspaces.\n"+
					"Do you want to copy only your current workspace?",
				opts.DestinationType),
			Description: fmt.Sprintf(
				strings.TrimSpace(inputBackendMigrateMultiToSingle),
				opts.SourceType, opts.DestinationType, currentWorkspace),
		})
		if err != nil {
			return fmt.Errorf(
				"Error asking for state migration action: %w", err)
		}
	}

	if !migrate {
		return fmt.Errorf("Migration aborted by user.")
	}

	// Copy the default state
	opts.sourceWorkspace = currentWorkspace

	// now switch back to the default env so we can access the new backend
	if err := m.SetWorkspace(backend.DefaultStateName); err != nil {
		return err
	}

	return m.backendMigrateState_s_s(ctx, opts)
}

// Single state to single state, assumed default state name.
func (m *Meta) backendMigrateState_s_s(ctx context.Context, opts *backendMigrateOpts) error {
	log.Printf("[INFO] backendMigrateState: single-to-single migrating %q workspace to %q workspace", opts.sourceWorkspace, opts.destinationWorkspace)

	sourceState, err := opts.Source.StateMgr(ctx, opts.sourceWorkspace)
	if err != nil {
		return fmt.Errorf(strings.TrimSpace(
			errMigrateSingleLoadDefault), opts.SourceType, err)
	}
	if err := sourceState.RefreshState(context.TODO()); err != nil {
		return fmt.Errorf(strings.TrimSpace(
			errMigrateSingleLoadDefault), opts.SourceType, err)
	}

	// Do not migrate workspaces without state.
	if sourceState.State().Empty() {
		log.Print("[TRACE] backendMigrateState: source workspace has empty state, so nothing to migrate")
		return nil
	}

	destinationState, err := opts.Destination.StateMgr(ctx, opts.destinationWorkspace)
	if err == backend.ErrDefaultWorkspaceNotSupported {
		// If the backend doesn't support using the default state, we ask the user
		// for a new name and migrate the default state to the given named state.
		destinationState, err = func() (statemgr.Full, error) {
			log.Print("[TRACE] backendMigrateState: destination doesn't support a default workspace, so we must prompt for a new name")
			name, err := m.promptNewWorkspaceName(opts.DestinationType)
			if err != nil {
				return nil, err
			}

			// Update the name of the destination state.
			opts.destinationWorkspace = name

			destinationState, err := opts.Destination.StateMgr(ctx, opts.destinationWorkspace)
			if err != nil {
				return nil, err
			}

			// Ignore invalid workspace name as it is irrelevant in this context.
			workspace, _ := m.Workspace(ctx)

			// If the currently selected workspace is the default workspace, then set
			// the named workspace as the new selected workspace.
			if workspace == backend.DefaultStateName {
				if err := m.SetWorkspace(opts.destinationWorkspace); err != nil {
					return nil, fmt.Errorf("Failed to set new workspace: %w", err)
				}
			}

			return destinationState, nil
		}()
	}
	if err != nil {
		return fmt.Errorf(strings.TrimSpace(
			errMigrateSingleLoadDefault), opts.DestinationType, err)
	}
	if err := destinationState.RefreshState(context.TODO()); err != nil {
		return fmt.Errorf(strings.TrimSpace(
			errMigrateSingleLoadDefault), opts.DestinationType, err)
	}

	// Check if we need migration at all.
	// This is before taking a lock, because they may also correspond to the same lock.
	source := sourceState.State()
	destination := destinationState.State()

	// no reason to migrate if the state is already there
	if source.Equal(destination) {
		// Equal isn't identical; it doesn't check lineage.
		sm1, _ := sourceState.(statemgr.PersistentMeta)
		sm2, _ := destinationState.(statemgr.PersistentMeta)
		if source != nil && destination != nil {
			if sm1 == nil || sm2 == nil {
				log.Print("[TRACE] backendMigrateState: both source and destination workspaces have no state, so no migration is needed")
				return nil
			}
			if sm1.StateSnapshotMeta().Lineage == sm2.StateSnapshotMeta().Lineage {
				log.Printf("[TRACE] backendMigrateState: both source and destination workspaces have equal state with lineage %q, so no migration is needed", sm1.StateSnapshotMeta().Lineage)
				return nil
			}
		}
	}

	if m.stateArgs.Lock {
		lockCtx := context.Background()
		view := opts.backendView(m.View).StateLocker()
		locker := clistate.NewLocker(m.stateArgs.LockTimeout, view)

		lockerSource := locker.WithContext(lockCtx)
		if diags := lockerSource.Lock(sourceState, "migration source state"); diags.HasErrors() {
			return diags.Err()
		}
		defer lockerSource.Unlock()

		lockerDestination := locker.WithContext(lockCtx)
		if diags := lockerDestination.Lock(destinationState, "migration destination state"); diags.HasErrors() {
			return diags.Err()
		}
		defer lockerDestination.Unlock()

		// We now own a lock, so double check that we have the version
		// corresponding to the lock.
		log.Print("[TRACE] backendMigrateState: refreshing source workspace state")
		if err := sourceState.RefreshState(context.TODO()); err != nil {
			return fmt.Errorf(strings.TrimSpace(
				errMigrateSingleLoadDefault), opts.SourceType, err)
		}
		log.Print("[TRACE] backendMigrateState: refreshing destination workspace state")
		if err := destinationState.RefreshState(context.TODO()); err != nil {
			return fmt.Errorf(strings.TrimSpace(
				errMigrateSingleLoadDefault), opts.SourceType, err)
		}

		source = sourceState.State()
		destination = destinationState.State()
	}

	var confirmFunc func(statemgr.Full, statemgr.Full, *backendMigrateOpts) (bool, error)
	switch {
	// No migration necessary
	case source.Empty() && destination.Empty():
		log.Print("[TRACE] backendMigrateState: both source and destination workspaces have empty state, so no migration is required")
		return nil

	// No migration necessary if we're inheriting state.
	case source.Empty() && !destination.Empty():
		log.Print("[TRACE] backendMigrateState: source workspace has empty state, so no migration is required")
		return nil

	// We have existing state moving into no state. Ask the user if
	// they'd like to do this.
	case !source.Empty() && destination.Empty():
		log.Print("[TRACE] backendMigrateState: destination workspace has empty state, so might copy source workspace state")
		confirmFunc = m.backendMigrateEmptyConfirm

	// Both states are non-empty, meaning we need to determine which
	// state should be used and update accordingly.
	case !source.Empty() && !destination.Empty():
		log.Print("[TRACE] backendMigrateState: both source and destination workspaces have states, so might overwrite destination with source")
		confirmFunc = m.backendMigrateNonEmptyConfirm
	}

	if confirmFunc == nil {
		panic("confirmFunc must not be nil")
	}

	if !opts.force {
		// Abort if we can't ask for input.
		if !m.input {
			log.Print("[TRACE] backendMigrateState: can't prompt for input, so aborting migration")
			return errors.New(strings.TrimSpace(errInteractiveInputDisabled))
		}

		// Confirm with the user whether we want to copy state over
		confirm, err := confirmFunc(sourceState, destinationState, opts)
		if err != nil {
			log.Print("[TRACE] backendMigrateState: error reading input, so aborting migration")
			return err
		}
		if !confirm {
			log.Print("[TRACE] backendMigrateState: user cancelled at confirmation prompt, so aborting migration")
			return nil
		}
	}

	// Confirmed! We'll have the statemgr package handle the migration, which
	// includes preserving any lineage/serial information where possible, if
	// both managers support such metadata.
	log.Print("[TRACE] backendMigrateState: migration confirmed, so migrating")
	if err := statemgr.Migrate(destinationState, sourceState); err != nil {
		return fmt.Errorf(strings.TrimSpace(errBackendStateCopy),
			opts.SourceType, opts.DestinationType, err)
	}
	// The backend is currently handled before providers are installed during init,
	// so requiring schemas here could lead to a catch-22 where it requires some manual
	// intervention to proceed far enough for provider installation. To avoid this,
	// when migrating to TFC backend, the initial JSON variant of state won't be generated and stored.
	if err := destinationState.PersistState(context.TODO(), nil); err != nil {
		return fmt.Errorf(strings.TrimSpace(errBackendStateCopy),
			opts.SourceType, opts.DestinationType, err)
	}

	// And we're done.
	return nil
}

func (m *Meta) backendMigrateEmptyConfirm(source, destination statemgr.Full, opts *backendMigrateOpts) (bool, error) {
	inputOpts := &tofu.InputOpts{
		Id:    "backend-migrate-copy-to-empty",
		Query: "Do you want to copy existing state to the new backend?",
		Description: fmt.Sprintf(
			strings.TrimSpace(inputBackendMigrateEmpty),
			opts.SourceType, opts.DestinationType),
	}

	return m.confirm(inputOpts)
}

func (m *Meta) backendMigrateNonEmptyConfirm(
	sourceState, destinationState statemgr.Full, opts *backendMigrateOpts) (bool, error) {
	// We need to grab both states so we can write them to a file
	source := sourceState.State()
	destination := destinationState.State()

	// Save both to a temporary
	td, err := os.MkdirTemp("", "terraform")
	if err != nil {
		return false, fmt.Errorf("Error creating temporary directory: %w", err)
	}
	defer os.RemoveAll(td)

	// Helper to write the state
	saveHelper := func(n, path string, s *states.State) error {
		return statemgr.WriteAndPersist(context.TODO(), statemgr.NewFilesystem(path, encryption.StateEncryptionDisabled()), s, nil)
	}

	// Write the states
	sourcePath := filepath.Join(td, fmt.Sprintf("1-%s.tfstate", opts.SourceType))
	destinationPath := filepath.Join(td, fmt.Sprintf("2-%s.tfstate", opts.DestinationType))
	if err := saveHelper(opts.SourceType, sourcePath, source); err != nil {
		return false, fmt.Errorf("Error saving temporary state: %w", err)
	}
	if err := saveHelper(opts.DestinationType, destinationPath, destination); err != nil {
		return false, fmt.Errorf("Error saving temporary state: %w", err)
	}

	// Ask for confirmation
	inputOpts := &tofu.InputOpts{
		Id:    "backend-migrate-to-backend",
		Query: "Do you want to copy existing state to the new backend?",
		Description: fmt.Sprintf(
			strings.TrimSpace(inputBackendMigrateNonEmpty),
			opts.SourceType, opts.DestinationType, sourcePath, destinationPath),
	}

	// Confirm with the user that the copy should occur
	return m.confirm(inputOpts)
}

func retrieveWorkspaces(ctx context.Context, back backend.Backend, sourceType string) ([]string, bool, error) {
	var singleState bool
	var err error
	workspaces, err := back.Workspaces(ctx)
	if err == backend.ErrWorkspacesNotSupported {
		singleState = true
		err = nil
	}
	if err != nil {
		return nil, singleState, fmt.Errorf(strings.TrimSpace(
			errMigrateLoadStates), sourceType, err)
	}

	return workspaces, singleState, err
}

func (m *Meta) promptNewWorkspaceName(destinationType string) (string, error) {
	message := fmt.Sprintf("[reset][bold][yellow]The %q backend configuration only allows "+
		"named workspaces![reset]", destinationType)
	if destinationType == "cloud" {
		if !m.input {
			log.Print("[TRACE] backendMigrateState: can't prompt for input, so aborting migration")
			return "", errors.New(strings.TrimSpace(errInteractiveInputDisabled))
		}
		message = `[reset][bold][yellow]Cloud backend requires all workspaces to be given an explicit name.[reset]`
	}
	name, err := m.UIInput().Input(context.Background(), &tofu.InputOpts{
		Id:          "new-state-name",
		Query:       message,
		Description: strings.TrimSpace(inputBackendNewWorkspaceName),
	})
	if err != nil {
		return "", fmt.Errorf("Error asking for new state name: %w", err)
	}

	return name, nil
}

const errMigrateLoadStates = `
Error inspecting states in the %q backend:
    %w

Prior to changing backends, OpenTofu inspects the source and destination
states to determine what kind of migration steps need to be taken, if any.
OpenTofu failed to load the states. The data in both the source and the
destination remain unmodified. Please resolve the above error and try again.
`

const errMigrateSingleLoadDefault = `
Error loading default state from the %q backend:
    %w

State migration cannot occur unless the state can be loaded. Backend
modification and state migration has been aborted. The state in both the
source and the destination remain unmodified. Please resolve the
above error and try again.
`

const errMigrateMulti = `
Error migrating the workspace %q from the previous %q backend
to the newly configured %q backend:
    %w

OpenTofu copies workspaces in alphabetical order. Any workspaces
alphabetically earlier than this one have been copied. Any workspaces
later than this haven't been modified in the destination. No workspaces
in the source state have been modified.

Please resolve the error above and run the initialization command again.
This will attempt to copy (with permission) all workspaces again.
`

const errBackendStateCopy = `
Error copying state from the previous %q backend to the newly configured
%q backend:
    %w

The state in the previous backend remains intact and unmodified. Please resolve
the error above and try again.
`

const errInteractiveInputDisabled = `
Can't ask approval for state migration when interactive input is disabled.

If non-interactive operation is desired, consider adding the "-force-copy"
option. Otherwise, please remove the "-input=false" option and try again.
`

const inputBackendMigrateEmpty = `
Pre-existing state was found while migrating the previous %q backend to the
newly configured %q backend. No existing state was found in the newly
configured %[2]q backend. Do you want to copy this state to the new %[2]q
backend? Enter "yes" to copy and "no" to start with an empty state.
`

const inputBackendMigrateNonEmpty = `
Pre-existing state was found while migrating the previous %q backend to the
newly configured %q backend. An existing non-empty state already exists in
the new backend. The two states have been saved to temporary files that will be
removed after responding to this query.

Previous (type %[1]q): %[3]s
New      (type %[2]q): %[4]s

Do you want to overwrite the state in the new backend with the previous state?
Enter "yes" to copy and "no" to start with the existing state in the newly
configured %[2]q backend.
`

const inputBackendMigrateMultiToSingle = `
The existing %[1]q backend supports workspaces and you currently are
using more than one. The newly configured %[2]q backend doesn't support
workspaces. If you continue, OpenTofu will copy your current workspace %[3]q
to the default workspace in the new backend. Your existing workspaces in the
source backend won't be modified. If you want to switch workspaces, back them
up, or cancel altogether, answer "no" and OpenTofu will abort.
`

const inputBackendMigrateMultiToMulti = `
Both the existing %[1]q backend and the newly configured %[2]q backend
support workspaces. When migrating between backends, OpenTofu will copy
all workspaces (with the same names). THIS WILL OVERWRITE any conflicting
states in the destination.

OpenTofu initialization doesn't currently migrate only select workspaces.
If you want to migrate a select number of workspaces, you must manually
pull and push those states.

If you answer "yes", OpenTofu will migrate all states. If you answer
"no", OpenTofu will abort.
`

const inputBackendNewWorkspaceName = `
Please provide a new workspace name (e.g. dev, test) that will be used
to migrate the existing default workspace.
`

const inputBackendSelectWorkspace = `
This is expected behavior when the selected workspace did not have an
existing non-empty state. Please enter a number to select a workspace:

%s
`
