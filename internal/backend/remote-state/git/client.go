// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package git

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/bogware/bogform/internal/states/remote"
	"github.com/bogware/bogform/internal/states/statemgr"
)

// lockBranchPrefix names the branches that act as locks. Hosting services accept any branch, while
// some reject custom ref namespaces, so these are ordinary branches.
const lockBranchPrefix = "refs/heads/bogform-locks/"

// maxPushAttempts limits retries when another writer updates the state branch at the same moment.
const maxPushAttempts = 5

// repository is a private bare clone used to talk to the remote without touching the user's checkout.
type repository struct {
	url, branch, dir string
	// credentialConfig is git configuration from the user's checkout that the private clone needs, as
	// GIT_CONFIG_* environment variables. See checkoutCredentials.
	credentialConfig []string
	// mu serializes use of the clone, whose index and fetched refs aren't safe to share.
	mu sync.Mutex
}

// emptyTree is the well-known ID of Git's empty tree.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func (r *repository) init(ctx context.Context) error {
	_, err := r.git(ctx, nil, "init", "--quiet", "--bare", ".")
	return err
}

// git runs a git command in the private clone. Prompts are disabled so a missing credential fails
// quickly rather than hanging the pipeline.
func (r *repository) git(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.dir
	cmd.Env = r.environment()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &gitError{args: args[0], stderr: strings.TrimSpace(stderr.String()), err: err}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (r *repository) environment() []string {
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Bogform", "GIT_AUTHOR_EMAIL=bogform@localhost",
		"GIT_COMMITTER_NAME=Bogform", "GIT_COMMITTER_EMAIL=bogform@localhost",
	)
	return append(env, r.credentialConfig...)
}

// checkoutCredentials finds credentials configured only in the current repository. CI systems set these
// on their checkout, for example actions/checkout's extraheader, and the private clone wouldn't otherwise
// see them. They're passed in the environment, which isn't visible in process listings as arguments are.
func checkoutCredentials(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "git", "config", "--local", "--includes", "--get-regexp", `^http\..*\.extraheader$`).Output()
	if err != nil {
		return nil
	}
	var env []string
	count := 0
	for line := range strings.Lines(string(out)) {
		key, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", count, key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, value))
		count++
	}
	if count == 0 {
		return nil
	}
	return append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", count))
}

// gitError reports a failed command. It leaves out the arguments, because they can include a URL
// with embedded credentials.
type gitError struct {
	args, stderr string
	err          error
}

func (e *gitError) Error() string {
	return fmt.Sprintf("git %s failed: %s: %s", e.args, e.err, e.stderr)
}

func (e *gitError) Unwrap() error { return e.err }

// remoteRef returns the commit that a ref on the remote points to, or "" if it doesn't exist.
func (r *repository) remoteRef(ctx context.Context, ref string) (string, error) {
	out, err := r.git(ctx, nil, "ls-remote", "--", r.url, ref)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(out) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref {
			return fields[0], nil
		}
	}
	return "", nil
}

// fetchHead downloads the state branch and returns its commit, or "" if it doesn't exist yet.
func (r *repository) fetchHead(ctx context.Context) (string, error) {
	ref := "refs/heads/" + r.branch
	head, err := r.remoteRef(ctx, ref)
	if err != nil || head == "" {
		return "", err
	}
	// Only the latest commit is needed, however long the state's history has grown.
	if _, err := r.git(ctx, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", r.url, ref); err != nil {
		return "", err
	}
	// Another writer could have pushed since the check above. Use what was actually fetched.
	return r.git(ctx, nil, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
}

func (r *repository) listFiles(ctx context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, err := r.fetchHead(ctx)
	if err != nil || head == "" {
		return nil, err
	}
	out, err := r.git(ctx, nil, "ls-tree", "-r", "--name-only", head)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// updateFile commits a change to one file on the state branch, retrying if another writer gets there
// first. A nil content removes the file. The push is a compare-and-swap on the branch's old value, so
// a concurrent update can't be lost.
func (r *repository) updateFile(ctx context.Context, path string, content []byte, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var lastErr error
	for attempt := range maxPushAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		head, err := r.fetchHead(ctx)
		if err != nil {
			return err
		}
		commit, err := r.commitFile(ctx, head, path, content, message)
		if err != nil {
			return err
		}
		if commit == "" {
			return nil // Nothing to change.
		}
		ref := "refs/heads/" + r.branch
		_, err = r.git(ctx, nil, "push", "--quiet", "--force-with-lease="+ref+":"+head, "--", r.url, commit+":"+ref)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRejected(err) {
			return err
		}
	}
	return fmt.Errorf("couldn't update the state branch after %d attempts: %w", maxPushAttempts, lastErr)
}

// commitFile builds a commit containing the change, without needing a working tree.
func (r *repository) commitFile(ctx context.Context, parent, path string, content []byte, message string) (string, error) {
	indexFile, err := os.CreateTemp("", "bogform-git-index-")
	if err != nil {
		return "", err
	}
	indexPath := indexFile.Name()
	indexFile.Close()
	// Git treats an empty file as a corrupt index, so remove it and let Git create a new one.
	os.Remove(indexPath)
	defer os.Remove(indexPath)

	run := func(stdin []byte, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = r.dir
		cmd.Env = append(r.environment(), "GIT_INDEX_FILE="+indexPath)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "", &gitError{args: args[0], stderr: strings.TrimSpace(stderr.String()), err: err}
		}
		return strings.TrimSpace(stdout.String()), nil
	}

	if parent != "" {
		if _, err := run(nil, "read-tree", parent); err != nil {
			return "", err
		}
	}
	if content == nil {
		// A zero mode removes the entry. Unlike --force-remove, this works without a working tree.
		entry := "0 0000000000000000000000000000000000000000\t" + path + "\n"
		if _, err := run([]byte(entry), "update-index", "--index-info"); err != nil {
			return "", err
		}
	} else {
		blob, err := run(content, "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return "", err
		}
		if _, err := run(nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path); err != nil {
			return "", err
		}
	}
	tree, err := run(nil, "write-tree")
	if err != nil {
		return "", err
	}
	if parent != "" {
		oldTree, err := run(nil, "rev-parse", parent+"^{tree}")
		if err != nil {
			return "", err
		}
		if oldTree == tree {
			return "", nil
		}
	}
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	return run(nil, args...)
}

func isRejected(err error) bool {
	var gitErr *gitError
	if !errors.As(err, &gitErr) {
		return false
	}
	message := strings.ToLower(gitErr.stderr)
	for _, sign := range []string{"rejected", "stale info", "non-fast-forward", "fetch first", "cannot lock ref", "failed to update ref", "reference already exists"} {
		if strings.Contains(message, sign) {
			return true
		}
	}
	return false
}

// Client reads and writes one workspace's state.
type Client struct {
	repo             *repository
	path             string
	lockName         string
	allowUnencrypted bool

	lockCommit string
}

var (
	_ remote.Client       = (*Client)(nil)
	_ remote.ClientLocker = (*Client)(nil)
)

// Get returns the current state, or nil if there isn't any.
func (c *Client) Get(ctx context.Context) (*remote.Payload, error) {
	c.repo.mu.Lock()
	defer c.repo.mu.Unlock()
	head, err := c.repo.fetchHead(ctx)
	if err != nil || head == "" {
		return nil, err
	}
	listing, err := c.repo.git(ctx, nil, "ls-tree", head, "--", c.path)
	if err != nil {
		return nil, err
	}
	if listing == "" {
		return nil, nil
	}
	blob := strings.Fields(listing)[2]
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", blob)
	cmd.Dir = c.repo.dir
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("couldn't read %s from the state branch: %w", c.path, err)
	}
	sum := md5.Sum(data)
	return &remote.Payload{Data: data, MD5: sum[:]}, nil
}

// isEncrypted reports whether data is in Bogform's encrypted state format.
func isEncrypted(data []byte) bool {
	var envelope struct {
		Data    []byte `json:"encrypted_data"`
		Version string `json:"encryption_version"`
	}
	return json.Unmarshal(data, &envelope) == nil && len(envelope.Data) > 0 && envelope.Version != ""
}

// Put writes the state. State can hold secrets and Git keeps every version forever, so unencrypted
// state is refused unless the configuration explicitly allows it.
func (c *Client) Put(ctx context.Context, data []byte) error {
	if !c.allowUnencrypted && !isEncrypted(data) {
		return errors.New(`the "git" backend refused to store unencrypted state, because anyone who can read the repository could read it, and Git would keep it permanently. Configure state encryption in your "terraform" block, or set allow_unencrypted = true in the backend if this state holds no secrets`)
	}
	return c.repo.updateFile(ctx, c.path, data, "Update "+c.path)
}

// Delete removes the state from the branch.
func (c *Client) Delete(ctx context.Context) error {
	return c.repo.updateFile(ctx, c.path, nil, "Delete "+c.path)
}

func (c *Client) lockRef() string {
	// Workspace names can contain characters that aren't valid in a ref, so use a hash.
	sum := md5.Sum([]byte(c.lockName + "\x00" + c.path))
	return lockBranchPrefix + hex.EncodeToString(sum[:])
}

// Lock acquires the lock by creating a branch. Git only accepts the push if the branch doesn't exist
// yet, so exactly one of any number of concurrent runs succeeds.
func (c *Client) Lock(ctx context.Context, info *statemgr.LockInfo) (string, error) {
	c.repo.mu.Lock()
	defer c.repo.mu.Unlock()

	info.Path = c.path
	commit, err := c.repo.git(ctx, nil, "commit-tree", emptyTree, "-m", string(info.Marshal()))
	if err != nil {
		// The empty tree might not be in this clone yet.
		if _, hashErr := c.repo.git(ctx, []byte{}, "hash-object", "-w", "-t", "tree", "--stdin"); hashErr != nil {
			return "", hashErr
		}
		commit, err = c.repo.git(ctx, nil, "commit-tree", emptyTree, "-m", string(info.Marshal()))
		if err != nil {
			return "", err
		}
	}
	ref := c.lockRef()
	if _, err := c.repo.git(ctx, nil, "push", "--quiet", "--force-with-lease="+ref+":", "--", c.repo.url, commit+":"+ref); err != nil {
		if !isRejected(err) {
			return "", err
		}
		lockErr := &statemgr.LockError{Err: errors.New("the state is locked by another run")}
		if holder, infoErr := c.lockHolder(ctx); infoErr == nil && holder != nil {
			lockErr.Info = holder
		}
		return "", lockErr
	}
	c.lockCommit = commit
	return info.ID, nil
}

func (c *Client) lockHolder(ctx context.Context) (*statemgr.LockInfo, error) {
	ref := c.lockRef()
	commit, err := c.repo.remoteRef(ctx, ref)
	if err != nil || commit == "" {
		return nil, err
	}
	if _, err := c.repo.git(ctx, nil, "fetch", "--quiet", "--no-tags", "--", c.repo.url, ref); err != nil {
		return nil, err
	}
	message, err := c.repo.git(ctx, nil, "log", "-1", "--format=%B", commit)
	if err != nil {
		return nil, err
	}
	var info statemgr.LockInfo
	if err := json.Unmarshal([]byte(message), &info); err != nil {
		return nil, fmt.Errorf("couldn't read the lock's details: %w", err)
	}
	return &info, nil
}

// Unlock releases the lock if the ID matches. Deleting with a lease means a lock taken over by someone
// else in the meantime isn't removed by mistake.
func (c *Client) Unlock(ctx context.Context, id string) error {
	c.repo.mu.Lock()
	defer c.repo.mu.Unlock()

	holder, err := c.lockHolder(ctx)
	if err != nil {
		return &statemgr.LockError{Err: err}
	}
	if holder == nil {
		return &statemgr.LockError{Err: errors.New("the state isn't locked")}
	}
	if holder.ID != id {
		return &statemgr.LockError{Info: holder, Err: fmt.Errorf("the lock ID %q doesn't match the current lock %q", id, holder.ID)}
	}
	ref := c.lockRef()
	commit, err := c.repo.remoteRef(ctx, ref)
	if err != nil {
		return &statemgr.LockError{Info: holder, Err: err}
	}
	if _, err := c.repo.git(ctx, nil, "push", "--quiet", "--force-with-lease="+ref+":"+commit, "--", c.repo.url, ":"+ref); err != nil {
		return &statemgr.LockError{Info: holder, Err: err}
	}
	c.lockCommit = ""
	return nil
}
