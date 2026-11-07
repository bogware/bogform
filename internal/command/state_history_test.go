// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"testing"

	backendLocal "github.com/bogware/bogform/internal/backend/local"
	"github.com/bogware/bogform/internal/states"
)

func TestShortVersionID(t *testing.T) {
	cases := map[string]string{
		"9b982f9d97481c0ad3e1a5e0ec9e66f6d5e8b1aa": "9b982f9d9748",
		"3sxZ.Pd1kB3lq2h_gn9u4pqvMkQH0RaV":         "3sxZ.Pd1kB3lq2h_gn9u4pqvMkQH0RaV", // An S3 version ID.
		"9b982f9d9748":                             "9b982f9d9748",
	}
	for in, want := range cases {
		if got := shortVersionID(in); got != want {
			t.Errorf("shortVersionID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatStateSize(t *testing.T) {
	cases := map[int64]string{-1: "deleted", 0: "0 B", 1003: "1003 B", 1536: "1.5 KB", 3 * 1024 * 1024: "3.0 MB"}
	for in, want := range cases {
		if got := formatStateSize(in); got != want {
			t.Errorf("formatStateSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestStateHistoryOfLocalBackend(t *testing.T) {
	// The local backend keeps no history, and neither does a local wrapper without a remote backend.
	if _, ok := stateHistoryOf(backendLocal.New(nil)); ok {
		t.Fatal("the local backend shouldn't report history support")
	}
}

func TestCountResourceInstances(t *testing.T) {
	if countResourceInstances(nil) != 0 {
		t.Fatal("a nil state has no instances")
	}
	if countResourceInstances(states.NewState()) != 0 {
		t.Fatal("an empty state has no instances")
	}
}
