// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package git

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/bogware/bogform/internal/backend"
)

func TestStateHistory(t *testing.T) {
	url := testRepository(t)
	b := testBackend(t, unencrypted(url))
	ctx := t.Context()

	versions, err := b.StateVersions(ctx, backend.DefaultStateName, 0)
	if err != nil || len(versions) != 0 {
		t.Fatalf("expected no versions before any state, got %v, %v", versions, err)
	}

	client := b.client(backend.DefaultStateName)
	states := []string{`{"serial":1}`, `{"serial":2}`, `{"serial":3}`}
	for _, s := range states {
		if err := client.Put(ctx, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	// A different workspace's state must not show up in the default workspace's history.
	if err := b.client("other").Put(ctx, []byte(`{"serial":9}`)); err != nil {
		t.Fatal(err)
	}

	// A new backend starts from a fresh clone, as a separate command would.
	b = testBackend(t, unencrypted(url))
	versions, err = b.StateVersions(ctx, backend.DefaultStateName, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(versions))
	}
	if !versions[0].Current || versions[1].Current {
		t.Fatal("only the newest version should be current")
	}
	for i, v := range versions {
		want := states[len(states)-1-i]
		if v.Size != int64(len(want)) {
			t.Errorf("version %d: size %d, want %d", i, v.Size, len(want))
		}
		data, err := b.StateVersionData(ctx, backend.DefaultStateName, v.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("version %d: got %s, want %s", i, data, want)
		}
	}

	limited, err := b.StateVersions(ctx, backend.DefaultStateName, 2)
	if err != nil || len(limited) != 2 {
		t.Fatalf("expected 2 versions with a limit, got %d, %v", len(limited), err)
	}

	// An abbreviated ID works.
	if _, err := b.StateVersionData(ctx, backend.DefaultStateName, versions[2].ID[:10]); err != nil {
		t.Fatalf("short ID: %s", err)
	}

	// The other workspace's commit isn't a version of the default workspace's state... but it is on the
	// branch, and the default state existed then, so it must return that state rather than fail.
	other, err := b.StateVersions(ctx, "other", 0)
	if err != nil || len(other) != 1 {
		t.Fatalf("expected 1 version of the other workspace, got %d, %v", len(other), err)
	}
	data, err := b.StateVersionData(ctx, backend.DefaultStateName, other[0].ID)
	if err != nil || string(data) != states[2] {
		t.Fatalf("got %s, %v", data, err)
	}

	// Deleting records a version with no data.
	if err := b.client("other").Delete(ctx); err != nil {
		t.Fatal(err)
	}
	other, err = b.StateVersions(ctx, "other", 0)
	if err != nil || len(other) != 2 {
		t.Fatalf("expected 2 versions after deleting, got %d, %v", len(other), err)
	}
	if other[0].Size != -1 || other[0].Current {
		t.Fatalf("the deletion should have no size and not be current: %+v", other[0])
	}
	if _, err := b.StateVersionData(ctx, "other", other[0].ID); !errors.Is(err, backend.ErrStateVersionDeleted) {
		t.Fatalf("expected ErrStateVersionDeleted, got %v", err)
	}
}

func TestStateVersionDataRejectsOtherCommits(t *testing.T) {
	url := testRepository(t)
	b := testBackend(t, unencrypted(url))
	ctx := t.Context()
	if err := b.client(backend.DefaultStateName).Put(ctx, []byte(`{"serial":1}`)); err != nil {
		t.Fatal(err)
	}

	// A commit on another branch, with a file at the state's path, mustn't be accepted as state.
	work := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %s", args, err, out)
		}
		return string(out)
	}
	run("init", "--quiet", "-b", "main")
	if err := writeFile(work+"/"+defaultPath, `{"serial":99}`); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "not state")
	run("push", "--quiet", url, "main")
	commit := run("rev-parse", "HEAD")[:40]

	b = testBackend(t, unencrypted(url))
	// Make sure the clone has the object, so the check isn't just "unknown commit".
	b.repo.mu.Lock()
	_, _ = b.repo.git(ctx, nil, "fetch", "--quiet", url, "main")
	b.repo.mu.Unlock()
	if _, err := b.StateVersionData(ctx, backend.DefaultStateName, commit); !errors.Is(err, backend.ErrStateVersionNotFound) {
		t.Fatalf("expected ErrStateVersionNotFound, got %v", err)
	}
	if _, err := b.StateVersionData(ctx, backend.DefaultStateName, "--output=x"); !errors.Is(err, backend.ErrStateVersionNotFound) {
		t.Fatalf("expected ErrStateVersionNotFound for an option, got %v", err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
