// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package backend

import (
	"context"
	"errors"
	"time"
)

// StateHistory is implemented by backends that keep earlier versions of a workspace's state, such as
// the git backend, whose branch keeps every version, and the s3 backend with bucket versioning.
type StateHistory interface {
	// StateVersions lists versions of a workspace's state, newest first. A limit of zero or less lists
	// them all.
	StateVersions(ctx context.Context, workspace string, limit int) ([]StateVersion, error)
	// StateVersionData returns one version's raw data, as the backend stores it. Encrypted state is
	// returned encrypted.
	StateVersionData(ctx context.Context, workspace, id string) ([]byte, error)
}

// StateVersion describes one stored version of a state.
type StateVersion struct {
	// ID identifies the version to StateVersionData. Backends accept unambiguous prefixes where they can.
	ID string `json:"id"`
	// Time is when the version was written.
	Time time.Time `json:"time"`
	// Size is the stored size in bytes, or -1 if the version removed the state.
	Size int64 `json:"size"`
	// Description is a short note from the backend, such as a commit message.
	Description string `json:"description,omitempty"`
	// Current is true for the version that's in use now.
	Current bool `json:"current"`
}

// ErrStateVersionNotFound is returned when a version doesn't exist, or doesn't belong to the workspace.
var ErrStateVersionNotFound = errors.New("there's no such version of this workspace's state")

// ErrStateVersionDeleted is returned for a version that removed the state, so has no data.
var ErrStateVersionDeleted = errors.New("that version removed the state, so there's nothing to restore")
