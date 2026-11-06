// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bogware/bogform/internal/backend"
	"github.com/bogware/bogform/internal/encryption"
	"github.com/bogware/bogform/internal/states/remote"
	"github.com/bogware/bogform/internal/states/statemgr"
)

func testRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't available")
	}
	dir := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s: %s", err, out)
	}
	return dir
}

func testBackend(t *testing.T, config map[string]any) *Backend {
	t.Helper()
	b := backend.TestBackendConfig(t, New(encryption.StateEncryptionDisabled()), backend.TestWrapConfig(config)).(*Backend)
	t.Cleanup(func() { os.RemoveAll(b.repo.dir) })
	return b
}

func unencrypted(url string) map[string]any {
	return map[string]any{"repository": url, "allow_unencrypted": true}
}

func TestBackendConformance(t *testing.T) {
	t.Run("states", func(t *testing.T) {
		backend.TestBackendStates(t, testBackend(t, unencrypted(testRepository(t))))
	})
	t.Run("locks", func(t *testing.T) {
		url := testRepository(t)
		backend.TestBackendStateLocks(t, testBackend(t, unencrypted(url)), testBackend(t, unencrypted(url)))
	})
	t.Run("force unlock", func(t *testing.T) {
		url := testRepository(t)
		backend.TestBackendStateForceUnlock(t, testBackend(t, unencrypted(url)), testBackend(t, unencrypted(url)))
	})
}

func TestClientConformance(t *testing.T) {
	url := testRepository(t)
	a, err := testBackend(t, unencrypted(url)).StateMgr(t.Context(), backend.DefaultStateName)
	if err != nil {
		t.Fatal(err)
	}
	remote.TestClient(t, a.(*remote.State).Client)

	b, err := testBackend(t, unencrypted(url)).StateMgr(t.Context(), backend.DefaultStateName)
	if err != nil {
		t.Fatal(err)
	}
	remote.TestRemoteLocks(t, a.(*remote.State).Client, b.(*remote.State).Client)
}

func TestPutRefusesUnencryptedState(t *testing.T) {
	url := testRepository(t)
	client := testBackend(t, map[string]any{"repository": url}).client(backend.DefaultStateName)

	err := client.Put(t.Context(), []byte(`{"version":4,"serial":1,"resources":[]}`))
	if err == nil || !strings.Contains(err.Error(), "refused to store unencrypted state") {
		t.Fatalf("wrong error: %v", err)
	}
	if head, err := client.repo.remoteRef(t.Context(), "refs/heads/"+defaultBranch); err != nil || head != "" {
		t.Fatalf("nothing should have been pushed: head %q, err %v", head, err)
	}

	encrypted := []byte(`{"meta":{},"encrypted_data":"c2VjcmV0","encryption_version":"v0"}`)
	if err := client.Put(t.Context(), encrypted); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != string(encrypted) {
		t.Fatalf("wrong data: %s", got.Data)
	}
}

func TestStateIsKeptOnItsOwnBranch(t *testing.T) {
	url := testRepository(t)
	client := testBackend(t, unencrypted(url)).client(backend.DefaultStateName)
	if err := client.Put(t.Context(), []byte(`{"version":4}`)); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "--git-dir", url, "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "refs/heads/"+defaultBranch {
		t.Fatalf("wrong refs: %q", got)
	}
}

func TestConcurrentLocksHaveOneWinner(t *testing.T) {
	url := testRepository(t)
	const contenders = 6
	var wins atomic.Int32
	var wg sync.WaitGroup
	clients := make([]*Client, contenders)
	for i := range clients {
		clients[i] = testBackend(t, unencrypted(url)).client(backend.DefaultStateName)
	}
	for _, client := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := statemgr.NewLockInfo()
			info.Operation = "test"
			if _, err := client.Lock(t.Context(), info); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("%d runs acquired the lock, want exactly 1", got)
	}
}

func TestConcurrentWritesAreNotLost(t *testing.T) {
	url := testRepository(t)
	const writers = 4
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		client := testBackend(t, unencrypted(url)).client(string(rune('a' + i)))
		client.path = "workspaces/" + client.lockName + "/" + defaultPath
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = client.Put(t.Context(), []byte(`{"version":4}`))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := testBackend(t, unencrypted(url)).Workspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != writers+1 {
		t.Fatalf("expected all %d writes to survive, got workspaces %v", writers, got)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := map[string]map[string]any{
		"branch with dots":     {"branch": "a..b"},
		"branch option":        {"branch": "--upload-pack=evil"},
		"path escaping branch": {"path": "../state"},
		"absolute path":        {"path": "/state"},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			config["repository"] = testRepository(t)
			_, _, errs := backend.TestBackendConfigWarningsAndErrors(t, New(encryption.StateEncryptionDisabled()), backend.TestWrapConfig(config))
			if len(errs) == 0 {
				t.Fatal("expected an error")
			}
		})
	}
}
