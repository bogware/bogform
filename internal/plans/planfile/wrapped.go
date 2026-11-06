// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package planfile

import (
	"github.com/opentofu/opentofu/internal/encryption"
)

// WrappedPlanFile represents a saved plan, loaded from a file path passed on the
// command line. It is expected to be used as a pointer, so that a nil value can
// represent the absence of any plan file.
type WrappedPlanFile struct {
	local *Reader
}

func (w *WrappedPlanFile) IsLocal() bool {
	return w != nil && w.local != nil
}

// Local checks whether the wrapped value is a local plan file, and returns it if available.
func (w *WrappedPlanFile) Local() (*Reader, bool) {
	if w != nil && w.local != nil {
		return w.local, true
	} else {
		return nil, false
	}
}

// NewWrappedLocal constructs a WrappedPlanFile from an already loaded local
// plan file reader. Most cases should use OpenWrapped to load from disk
// instead. If the provided reader is nil, the returned pointer is nil.
func NewWrappedLocal(l *Reader) *WrappedPlanFile {
	if l != nil {
		return &WrappedPlanFile{local: l}
	} else {
		return nil
	}
}

// OpenWrapped loads a local plan file from a specified file path, or returns an
// error if the file doesn't seem to be a plan file.
func OpenWrapped(filename string, enc encryption.PlanEncryption) (*WrappedPlanFile, error) {
	local, err := Open(filename, enc)
	if err != nil {
		return nil, err
	}
	return &WrappedPlanFile{local: local}, nil
}
