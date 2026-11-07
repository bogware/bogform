// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo makes a Git repository with the given files, and returns an Env for it.
func repo(t *testing.T, files map[string]string, environment map[string]string) *Env {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "--quiet", "--initial-branch=main")
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "add", "-A")
	env, err := NewEnv(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	env.Getenv = func(name string) string { return environment[name] }
	return env
}

func find(results []Result, id string) []Result {
	var found []Result
	for _, r := range results {
		if r.ID == id {
			found = append(found, r)
		}
	}
	return found
}

func status(t *testing.T, results []Result, id string, want Status) Result {
	t.Helper()
	found := find(results, id)
	if len(found) == 0 {
		t.Fatalf("no %s result in %+v", id, results)
	}
	for _, r := range found {
		if r.Status == want {
			return r
		}
	}
	t.Fatalf("%s: want %s, got %+v", id, want, found)
	return Result{}
}

const goodGitignore = ".terraform/\n.bogform-ci/\n"

func TestRun_emptyDirectory(t *testing.T) {
	env := repo(t, map[string]string{"README.md": "hi"}, nil)
	results := Run(context.Background(), env)
	status(t, results, "configuration", StatusProblem)
	if !HasProblems(results) {
		t.Fatal("should have a problem")
	}
}

func TestRun_unconfigured(t *testing.T) {
	env := repo(t, map[string]string{
		"main.tf": `terraform {
  required_providers {
    aws = { source = "hashicorp/aws" }
  }
}

variable "owner" {
  default = "someone@realcompany.com"
}

variable "placeholder" {
  default = "you@example.com"
}
`,
		"keys.tfvars":    "access_key = \"AKIAABCDEFGHIJKLMNOP\"\n",
		"contact.tfvars": "contact = \"person@realcompany.com\"\n",
	}, nil)
	results := Run(context.Background(), env)
	status(t, results, "configuration", StatusOK)
	status(t, results, "backend", StatusWarning)
	status(t, results, "encryption", StatusWarning)
	versions := status(t, results, "versions", StatusWarning)
	if !strings.Contains(versions.Summary, "aws") {
		t.Errorf("should name the unpinned provider: %s", versions.Summary)
	}
	status(t, results, "lock-file", StatusWarning)
	status(t, results, "pipeline", StatusWarning)
	status(t, results, "bogform-md", StatusWarning)
	status(t, results, "ignored", StatusWarning)
	secrets := status(t, results, "secrets", StatusProblem)
	if strings.Contains(secrets.Summary, "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(secrets.Summary, "keys.tfvars:1") {
		t.Errorf("should say where the key is, but not what it is: %s", secrets.Summary)
	}
	personal := status(t, results, "personal-information", StatusWarning)
	if strings.Contains(personal.Summary, "realcompany") || !strings.Contains(personal.Summary, "var.owner") || !strings.Contains(personal.Summary, "contact.tfvars:1") {
		t.Errorf("should say where the addresses are, but not what they are: %s", personal.Summary)
	}
	if strings.Contains(personal.Summary, "placeholder") {
		t.Errorf("example.com addresses aren't anyone's: %s", personal.Summary)
	}
}

func TestRun_wellConfigured(t *testing.T) {
	remote := t.TempDir()
	git(t, remote, "init", "--quiet", "--bare")
	env := repo(t, map[string]string{
		"main.tf": fmt.Sprintf(`terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  backend "git" {
    repository = %q
  }
  encryption {
    key_provider "passphrase" "state" {
      env      = "STATE_PASSPHRASE"
      keychain = "infra"
    }
    key_provider "pbkdf2" "state" {
      chain = key_provider.passphrase.state
    }
    method "aes_gcm" "state" {
      keys = key_provider.pbkdf2.state
    }
    state {
      method   = method.aes_gcm.state
      enforced = true
    }
  }
}
`, filepath.ToSlash(remote)),
		".terraform.lock.hcl":         "# lock\n",
		".gitignore":                  goodGitignore,
		"BOGFORM.md":                  "# guide\n",
		".github/workflows/infra.yml": "jobs:\n  plan:\n    steps:\n      - uses: bogware/bogform@v1.15.0\n        with:\n          version: 1.15.0\n",
	}, map[string]string{"STATE_PASSPHRASE": "correct horse battery staple"})
	results := Run(context.Background(), env)
	for _, r := range results {
		if r.Status != StatusOK && r.ID != "configuration" {
			t.Errorf("unexpected %s: %s", r.Status, r.Summary)
		}
	}
	backend := status(t, results, "backend", StatusOK)
	if !strings.Contains(backend.Summary, "no bogform-state branch yet") {
		t.Errorf("should note there's no state yet: %s", backend.Summary)
	}
	status(t, results, "passphrase-available", StatusOK)
	status(t, results, "locks", StatusOK)
	for _, r := range results {
		if strings.Contains(r.Summary, "correct horse") {
			t.Fatalf("a result shows the passphrase: %s", r.Summary)
		}
	}
}

func TestCheckEncryption(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  *staticConfig
		env  map[string]string
		id   string
		want Status
	}{
		"git without encryption": {cfg: &staticConfig{Backend: "git"}, id: "encryption", want: StatusProblem},
		"git allowing unencrypted": {
			cfg: &staticConfig{Backend: "git", BackendArgs: map[string]string{"allow_unencrypted": "true"}}, id: "encryption", want: StatusWarning,
		},
		"not enforced": {cfg: &staticConfig{Encryption: true}, id: "encryption", want: StatusWarning},
		"literal passphrase": {
			cfg: &staticConfig{Encryption: true, StateEnforced: true, KeyProviders: []keyProvider{{Type: "pbkdf2", Name: "s", PassphraseLiteral: true}}},
			id:  "passphrase", want: StatusProblem,
		},
		"variable not set": {
			cfg: &staticConfig{Encryption: true, StateEnforced: true, KeyProviders: []keyProvider{{Type: "pbkdf2", Name: "s", PassphraseVariable: "pass"}}},
			id:  "passphrase-available", want: StatusWarning,
		},
		"variable too short": {
			cfg: &staticConfig{Encryption: true, StateEnforced: true, KeyProviders: []keyProvider{{Type: "pbkdf2", Name: "s", PassphraseVariable: "pass"}}},
			env: map[string]string{"TF_VAR_pass": "short"}, id: "passphrase-available", want: StatusProblem,
		},
		"variable set": {
			cfg: &staticConfig{Encryption: true, StateEnforced: true, KeyProviders: []keyProvider{{Type: "pbkdf2", Name: "s", PassphraseVariable: "pass"}}},
			env: map[string]string{"BOG_VAR_pass": "a long enough passphrase"}, id: "passphrase-available", want: StatusOK,
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := &Env{Getenv: func(n string) string { return tc.env[n] }}
			results := append(checkEncryption(tc.cfg), checkPassphraseEnv(env, tc.cfg)...)
			status(t, results, tc.id, tc.want)
		})
	}
}

func TestCheckStaleLocks(t *testing.T) {
	remote := t.TempDir()
	git(t, remote, "init", "--quiet", "--bare")
	work := t.TempDir()
	git(t, work, "init", "--quiet")
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	message := fmt.Sprintf(`{"ID":"lock-123","Operation":"OperationTypeApply","Who":"runner@ci","Created":%q}`, created.Format(time.RFC3339))
	// Lock commits use the empty tree, which a new repository doesn't have yet.
	git(t, work, "hash-object", "-w", "-t", "tree", "--stdin")
	commit := git(t, work, "commit-tree", "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "-m", message)
	git(t, work, "push", "--quiet", remote, commit+":refs/heads/bogform-locks/abc")

	cfg := &staticConfig{Backend: "git", BackendArgs: map[string]string{"repository": filepath.ToSlash(remote)}}
	env, err := NewEnv(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	env.StaleLockAge = time.Hour

	env.Now = func() time.Time { return created.Add(10 * time.Minute) }
	if r, ok := checkStaleLocks(context.Background(), env, cfg); !ok || r.Status != StatusOK {
		t.Fatalf("a recent lock is fine: %+v", r)
	}
	env.Now = func() time.Time { return created.Add(5 * time.Hour) }
	r, ok := checkStaleLocks(context.Background(), env, cfg)
	if !ok || r.Status != StatusWarning || !strings.Contains(r.Summary, "lock-123") || !strings.Contains(r.Fix, "force-unlock") {
		t.Fatalf("an old lock should be reported with its ID: %+v", r)
	}
}

func TestCheckPipeline_unpinned(t *testing.T) {
	for name, content := range map[string]string{
		"action on main": "- uses: bogware/bogform@main\n",
		"latest version": "- uses: bogware/bogform@v1.15.0\n  with:\n    version: latest\n",
		"latest image":   "image:\n  name: ghcr.io/bogware/bogform:latest\n",
		"azure latest":   "variables:\n  bogformVersion: latest\n",
	} {
		t.Run(name, func(t *testing.T) {
			file := ".github/workflows/bogform.yml"
			if strings.Contains(content, "image:") {
				file = ".gitlab-ci.yml"
			} else if strings.Contains(content, "bogformVersion") {
				file = "azure-pipelines.yml"
			}
			env := repo(t, map[string]string{file: content}, nil)
			r := checkPipeline(env)
			if r.Status != StatusWarning || !strings.Contains(r.Summary, file) {
				t.Fatalf("should warn about %s: %+v", file, r)
			}
		})
	}
}

func TestReadStaticConfig(t *testing.T) {
	dir := t.TempDir()
	content := `terraform {
  required_providers {
    old = "~> 1.0"
    new = { source = "x/new", version = "2.0" }
    bare = { source = "x/bare" }
  }
  backend "s3" {
    bucket = "my-state"
    key    = "app/${terraform.workspace}"
  }
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.tofu"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := readStaticConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Providers["old"] || !cfg.Providers["new"] || cfg.Providers["bare"] {
		t.Errorf("wrong provider versions: %v", cfg.Providers)
	}
	if cfg.Backend != "s3" || cfg.BackendArgs["bucket"] != "my-state" {
		t.Errorf("wrong backend: %s %v", cfg.Backend, cfg.BackendArgs)
	}
	if _, ok := cfg.BackendArgs["key"]; ok {
		t.Error("an interpolated value isn't a literal")
	}
}
