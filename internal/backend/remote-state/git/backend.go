// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package git implements a state backend that keeps encrypted state on a dedicated branch of a Git
// repository, for small projects that don't want to set up any cloud storage.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/backend"
	"github.com/bogware/bogform/internal/encryption"
	"github.com/bogware/bogform/internal/legacy/helper/schema"
	"github.com/bogware/bogform/internal/states"
	"github.com/bogware/bogform/internal/states/remote"
	"github.com/bogware/bogform/internal/states/statemgr"
)

const (
	defaultBranch = "bogform-state"
	defaultPath   = "terraform.tfstate"
	// workspacesDir holds the state of workspaces other than the default one.
	workspacesDir = "workspaces"

	cloneDirPrefix = "bogform-git-state-"
	cloneMaxAge    = 24 * time.Hour
)

// New creates a new backend that stores state in a Git repository.
func New(enc encryption.StateEncryption) backend.Backend {
	s := &schema.Backend{
		Schema: map[string]*schema.Schema{
			"repository": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "URL of the Git repository. Defaults to the origin remote of the current repository.",
				DefaultFunc: schema.EnvDefaultFunc("BOG_GIT_STATE_REPOSITORY", ""),
			},
			"branch": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     defaultBranch,
				Description: "The branch that holds state. Use a branch that only Bogform writes to.",
			},
			"path": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     defaultPath,
				Description: "The path of the state file on the branch.",
			},
			"allow_unencrypted": {
				Type:        schema.TypeBool,
				Optional:    true,
				Default:     false,
				Description: "Allow storing unencrypted state. State can contain secrets, and anyone who can read the repository could read them.",
			},
		},
	}
	result := &Backend{Backend: s, encryption: enc}
	result.Backend.ConfigureFunc = result.configure
	return result
}

// Backend stores state on a branch of a Git repository.
type Backend struct {
	*schema.Backend
	encryption encryption.StateEncryption

	repo             *repository
	path             string
	allowUnencrypted bool
}

func (b *Backend) configure(ctx context.Context) error {
	data := schema.FromContextBackendConfig(ctx)

	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf(`the "git" backend needs the git command, which wasn't found: %w`, err)
	}

	url := data.Get("repository").(string)
	if url == "" {
		out, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
		if err != nil {
			return fmt.Errorf(`couldn't find the "origin" remote of this repository. Set "repository" in the backend configuration: %w`, err)
		}
		url = strings.TrimSpace(string(out))
	}

	branch := data.Get("branch").(string)
	statePath := data.Get("path").(string)
	if err := validateBranch(branch); err != nil {
		return err
	}
	if path.IsAbs(statePath) || path.Clean(statePath) != statePath || strings.HasPrefix(statePath, "..") {
		return fmt.Errorf("the path %q must be a relative path on the branch", statePath)
	}

	removeStaleClones()
	dir, err := os.MkdirTemp("", cloneDirPrefix+"*")
	if err != nil {
		return err
	}
	b.repo = &repository{url: url, branch: branch, dir: dir}
	if err := b.repo.init(ctx); err != nil {
		return err
	}
	b.path = statePath
	b.allowUnencrypted = data.Get("allow_unencrypted").(bool)
	return nil
}

// removeStaleClones deletes private clones left by earlier runs. Backends have no shutdown step, so a
// clone can't be removed when its run ends. The age limit leaves clones of running commands alone.
func removeStaleClones() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), cloneDirPrefix+"*"))
	if err != nil {
		return
	}
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && time.Since(info.ModTime()) > cloneMaxAge {
			_ = os.RemoveAll(match)
		}
	}
}

func validateBranch(branch string) error {
	if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsAny(branch, " ~^:?*[\\") || strings.Contains(branch, "..") {
		return fmt.Errorf("%q isn't a valid branch name", branch)
	}
	return nil
}

func (b *Backend) statePath(workspace string) string {
	if workspace == backend.DefaultStateName {
		return b.path
	}
	return path.Join(workspacesDir, workspace, b.path)
}

// Workspaces lists the workspaces that have state on the branch.
func (b *Backend) Workspaces(ctx context.Context) ([]string, error) {
	files, err := b.repo.listFiles(ctx)
	if err != nil {
		return nil, err
	}
	result := []string{backend.DefaultStateName}
	for _, file := range files {
		rest, found := strings.CutPrefix(file, workspacesDir+"/")
		if !found {
			continue
		}
		name, statePath, found := strings.Cut(rest, "/")
		if found && statePath == b.path && !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	slices.Sort(result[1:])
	return result, nil
}

// DeleteWorkspace removes a workspace's state from the branch. Earlier versions stay in Git's history.
func (b *Backend) DeleteWorkspace(ctx context.Context, name string, _ bool) error {
	if name == backend.DefaultStateName || name == "" {
		return fmt.Errorf("can't delete default state")
	}
	return b.client(name).Delete(ctx)
}

func (b *Backend) client(workspace string) *Client {
	return &Client{
		repo:             b.repo,
		path:             b.statePath(workspace),
		lockName:         workspace,
		allowUnencrypted: b.allowUnencrypted,
	}
}

// StateMgr returns the state manager for a workspace, creating an empty state if it's new.
func (b *Backend) StateMgr(ctx context.Context, name string) (statemgr.Full, error) {
	var stateMgr statemgr.Full = remote.NewState(b.client(name), b.encryption)

	existing, err := b.Workspaces(ctx)
	if err != nil {
		return nil, err
	}
	if name == backend.DefaultStateName || slices.Contains(existing, name) {
		return stateMgr, nil
	}

	// A new workspace needs a state on the branch so that it shows up in the list of workspaces.
	lockInfo := statemgr.NewLockInfo()
	lockInfo.Operation = "init"
	lockID, err := stateMgr.Lock(ctx, lockInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to lock state: %w", err)
	}
	unlock := func(cause error) error {
		if err := stateMgr.Unlock(ctx, lockID); err != nil {
			return fmt.Errorf("%w\n\nAlso, the state is still locked: %s", cause, err)
		}
		return cause
	}
	if err := stateMgr.RefreshState(ctx); err != nil {
		return nil, unlock(err)
	}
	if stateMgr.State() == nil {
		if err := stateMgr.WriteState(states.NewState()); err != nil {
			return nil, unlock(err)
		}
		if err := stateMgr.PersistState(ctx, nil); err != nil {
			return nil, unlock(err)
		}
	}
	if err := stateMgr.Unlock(ctx, lockID); err != nil {
		return nil, fmt.Errorf("failed to unlock state: %w", err)
	}
	return stateMgr, nil
}
