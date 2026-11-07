// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package git

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/backend"
)

var _ backend.StateHistory = (*Backend)(nil)

// fullDepth asks for the whole history. Unlike --unshallow, it works whether or not the clone is
// already shallow from an earlier fetch.
const fullDepth = "--depth=2147483647"

var commitPattern = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// fetchHistory downloads the state branch with all of its history, and returns its commit, or "" if
// the branch doesn't exist. The caller must hold r.mu.
func (r *repository) fetchHistory(ctx context.Context) (string, error) {
	ref := "refs/heads/" + r.branch
	head, err := r.remoteRef(ctx, ref)
	if err != nil || head == "" {
		return "", err
	}
	if _, err := r.git(ctx, nil, "fetch", "--quiet", "--no-tags", fullDepth, "--", r.url, ref); err != nil {
		return "", err
	}
	return r.git(ctx, nil, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
}

// StateVersions lists the commits on the state branch that changed the workspace's state.
func (b *Backend) StateVersions(ctx context.Context, workspace string, limit int) ([]backend.StateVersion, error) {
	if err := validateWorkspace(workspace); err != nil {
		return nil, err
	}
	statePath := b.statePath(workspace)

	b.repo.mu.Lock()
	defer b.repo.mu.Unlock()
	head, err := b.repo.fetchHistory(ctx)
	if err != nil || head == "" {
		return nil, err
	}
	args := []string{"log", "--format=%H%x00%ct%x00%s"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	args = append(args, head, "--", statePath)
	out, err := b.repo.git(ctx, nil, args...)
	if err != nil {
		return nil, err
	}

	var versions []backend.StateVersion
	for line := range strings.Lines(out) {
		fields := strings.SplitN(strings.TrimRight(line, "\r\n"), "\x00", 3)
		if len(fields) != 3 {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("couldn't read the time of commit %s: %w", fields[0], err)
		}
		size := int64(-1)
		if sizeOut, err := b.repo.git(ctx, nil, "cat-file", "-s", fields[0]+":"+statePath); err == nil {
			size, _ = strconv.ParseInt(sizeOut, 10, 64)
		}
		versions = append(versions, backend.StateVersion{
			ID:          fields[0],
			Time:        time.Unix(seconds, 0).UTC(),
			Size:        size,
			Description: fields[2],
		})
	}
	if len(versions) > 0 && versions[0].Size >= 0 {
		versions[0].Current = true
	}
	return versions, nil
}

// StateVersionData returns the workspace's state as it was at a commit on the state branch. The commit
// must be on the branch, so that a commit from elsewhere in the repository can't be passed off as state.
func (b *Backend) StateVersionData(ctx context.Context, workspace, id string) ([]byte, error) {
	if err := validateWorkspace(workspace); err != nil {
		return nil, err
	}
	if !commitPattern.MatchString(id) {
		return nil, fmt.Errorf("%q isn't a commit ID: %w", id, backend.ErrStateVersionNotFound)
	}
	statePath := b.statePath(workspace)

	b.repo.mu.Lock()
	defer b.repo.mu.Unlock()
	head, err := b.repo.fetchHistory(ctx)
	if err != nil {
		return nil, err
	}
	if head == "" {
		return nil, backend.ErrStateVersionNotFound
	}
	commit, err := b.repo.git(ctx, nil, "rev-parse", "--verify", "--quiet", id+"^{commit}")
	if err != nil || commit == "" {
		return nil, backend.ErrStateVersionNotFound
	}
	if _, err := b.repo.git(ctx, nil, "merge-base", "--is-ancestor", commit, head); err != nil {
		return nil, backend.ErrStateVersionNotFound
	}
	if _, err := b.repo.git(ctx, nil, "cat-file", "-e", commit+":"+statePath); err != nil {
		return nil, backend.ErrStateVersionDeleted
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", commit+":"+statePath)
	cmd.Dir = b.repo.dir
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("couldn't read %s at %s: %w", statePath, commit, err)
	}
	return data, nil
}
