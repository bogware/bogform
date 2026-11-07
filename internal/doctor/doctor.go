// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package doctor checks a configuration's repository setup and says how to fix what it finds.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/configs"
)

// Status is the outcome of one check.
type Status string

const (
	StatusOK      Status = "ok"
	StatusWarning Status = "warning"
	StatusProblem Status = "problem"
)

// Result is one check's outcome. Summaries never include secret values.
type Result struct {
	ID      string `json:"id"`
	Status  Status `json:"status"`
	Summary string `json:"summary"`
	Fix     string `json:"fix,omitempty"`
}

// Env is what the checks run against. Tests replace its functions.
type Env struct {
	// Dir is the configuration's directory.
	Dir string
	// Root is the repository's root, or "" if Dir isn't in a Git repository.
	Root   string
	Getenv func(string) string
	// Git runs a git command in Dir.
	Git func(ctx context.Context, args ...string) (string, error)
	Now func() time.Time
	// StaleLockAge is how old a lock must be to look abandoned.
	StaleLockAge time.Duration
}

// NewEnv returns an Env for a directory on this machine.
func NewEnv(ctx context.Context, dir string) (*Env, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	env := &Env{
		Dir:          dir,
		Getenv:       os.Getenv,
		Now:          time.Now,
		StaleLockAge: time.Hour,
		Git: func(ctx context.Context, args ...string) (string, error) {
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			out, err := cmd.Output()
			return strings.TrimSpace(string(out)), err
		},
	}
	if root, err := env.Git(ctx, "rev-parse", "--show-toplevel"); err == nil {
		env.Root = filepath.FromSlash(root)
	}
	return env, nil
}

// Run runs every check.
func Run(ctx context.Context, env *Env) []Result {
	cfg, err := readStaticConfig(env.Dir)
	if err != nil {
		return []Result{{ID: "configuration", Status: StatusProblem, Summary: "Couldn't read the directory: " + err.Error()}}
	}
	if len(cfg.Files) == 0 {
		return []Result{{
			ID: "configuration", Status: StatusProblem,
			Summary: "There's no configuration in " + env.Dir + ".",
			Fix:     "Run doctor in the directory with your .tf files, or use -chdir.",
		}}
	}
	results := []Result{checkConfiguration(env)}
	results = append(results, checkBackend(ctx, env, cfg))
	results = append(results, checkEncryption(cfg)...)
	results = append(results, checkPassphraseEnv(env, cfg)...)
	if r, ok := checkStaleLocks(ctx, env, cfg); ok {
		results = append(results, r)
	}
	results = append(results, checkVersions(cfg))
	if env.Root == "" {
		results = append(results, Result{
			ID: "repository", Status: StatusWarning,
			Summary: "This isn't in a Git repository, so the repository checks were skipped.",
			Fix:     "Keep your configuration in a repository, with a pipeline. See \"bogform ci init\".",
		})
		return results
	}
	results = append(results, checkLockFile(ctx, env))
	results = append(results, checkPipeline(env))
	results = append(results, checkBogformMD(env))
	results = append(results, checkIgnored(ctx, env)...)
	results = append(results, checkSecrets(ctx, env, cfg)...)
	return results
}

// HasProblems reports whether any result is a problem.
func HasProblems(results []Result) bool {
	for _, r := range results {
		if r.Status == StatusProblem {
			return true
		}
	}
	return false
}

func checkConfiguration(env *Env) Result {
	_, diags := configs.NewParser(nil).LoadConfigDir(env.Dir)
	if diags.HasErrors() {
		first := diags.Errs()[0].Error()
		return Result{ID: "configuration", Status: StatusProblem, Summary: "The configuration doesn't load: " + first, Fix: "Run \"bogform validate\" for details."}
	}
	return Result{ID: "configuration", Status: StatusOK, Summary: "The configuration loads."}
}

func checkBackend(ctx context.Context, env *Env, cfg *staticConfig) Result {
	switch cfg.Backend {
	case "", "local":
		return Result{
			ID: "backend", Status: StatusWarning,
			Summary: "State is kept on this machine, so a pipeline can't use it and it's lost with the machine.",
			Fix:     "Use a remote backend, such as s3, or git with encryption. See \"bogform ci init -git-state\".",
		}
	case "cloud", "remote":
		return Result{
			ID: "backend", Status: StatusProblem,
			Summary: "The " + cfg.Backend + " backend is for HCP Terraform, which Bogform doesn't support.",
			Fix:     "Move the state to another backend. \"bogform migrate tfc\" can copy it.",
		}
	case "git":
		url := cfg.BackendArgs["repository"]
		if url == "" {
			out, err := env.Git(ctx, "remote", "get-url", "origin")
			if err != nil || out == "" {
				return Result{ID: "backend", Status: StatusProblem, Summary: "The git backend has no repository: there's no \"origin\" remote.", Fix: "Add an origin remote, or set \"repository\" in the backend."}
			}
			url = out
		}
		branch := cfg.BackendArgs["branch"]
		if branch == "" {
			branch = "bogform-state"
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := env.Git(ctx, "ls-remote", "--", url, "refs/heads/"+branch)
		if err != nil {
			return Result{ID: "backend", Status: StatusProblem, Summary: "The git backend's repository can't be reached.", Fix: "Check that \"git ls-remote\" works for the repository, with your credentials."}
		}
		if out == "" {
			return Result{ID: "backend", Status: StatusOK, Summary: "The git backend's repository can be reached. It has no " + branch + " branch yet, which is expected before the first apply."}
		}
		return Result{ID: "backend", Status: StatusOK, Summary: "The git backend's repository can be reached, and has state on " + branch + "."}
	default:
		return Result{ID: "backend", Status: StatusOK, Summary: "State is in the " + cfg.Backend + " backend. Doctor doesn't check that it can be reached."}
	}
}

func checkEncryption(cfg *staticConfig) []Result {
	if !cfg.Encryption {
		if cfg.Backend == "git" && cfg.BackendArgs["allow_unencrypted"] != "true" {
			return []Result{{
				ID: "encryption", Status: StatusProblem,
				Summary: "The git backend refuses unencrypted state, and there's no encryption block.",
				Fix:     "Add an encryption block with enforced = true. See the git backend's documentation.",
			}}
		}
		return []Result{{
			ID: "encryption", Status: StatusWarning,
			Summary: "Bogform doesn't encrypt this state, so anyone who can read the backend can read any secrets in it.",
			Fix:     "Add an encryption block to the terraform block.",
		}}
	}
	results := []Result{}
	if cfg.StateEnforced {
		results = append(results, Result{ID: "encryption", Status: StatusOK, Summary: "State is encrypted, and enforced = true refuses to write it unencrypted."})
	} else {
		results = append(results, Result{ID: "encryption", Status: StatusWarning, Summary: "State encryption isn't enforced, so a mistake could write it unencrypted.", Fix: "Set enforced = true in the encryption's state block."})
	}
	for _, kp := range cfg.KeyProviders {
		if kp.Type == "pbkdf2" {
			results = append(results, checkPassphrase(cfg, kp))
		}
	}
	return results
}

// minimumPassphrase matches the pbkdf2 key provider's minimum.
const minimumPassphrase = 16

func checkPassphrase(cfg *staticConfig, kp keyProvider) Result {
	id := "passphrase"
	name := "key_provider.pbkdf2." + kp.Name
	switch {
	case kp.PassphraseLiteral:
		return Result{ID: id, Status: StatusProblem, Summary: "The passphrase for " + name + " is written in the configuration, so anyone who can read the repository can decrypt the state.", Fix: "Take it from a sensitive variable or the passphrase key provider, change the passphrase, and run \"bogform state rekey\"."}
	case kp.PassphraseVariable != "":
		return Result{ID: id, Status: StatusOK, Summary: "The passphrase for " + name + " comes from var." + kp.PassphraseVariable + ". Set it with TF_VAR_" + kp.PassphraseVariable + ", or a passphrase key provider."}
	case kp.Chain != "":
		chained, ok := cfg.keyProvider(kp.Chain)
		if !ok || chained.Type != "passphrase" {
			return Result{ID: id, Status: StatusOK, Summary: "The passphrase for " + name + " comes from key_provider." + kp.Chain + "."}
		}
		var sources []string
		for _, arg := range []string{"env", "keychain", "aws_ssm", "azure_key_vault"} {
			if chained.Args[arg] != "" {
				sources = append(sources, arg)
			}
		}
		return Result{ID: id, Status: StatusOK, Summary: "The passphrase for " + name + " comes from key_provider." + kp.Chain + " (" + strings.Join(sources, ", ") + "). Doctor doesn't read it."}
	}
	return Result{ID: id, Status: StatusOK, Summary: "The passphrase for " + name + " is set."}
}

// checkPassphraseEnv reports whether the passphrase is available in this environment, without
// revealing it. It's separate so that it's clear which checks look at the environment.
func checkPassphraseEnv(env *Env, cfg *staticConfig) []Result {
	var results []Result
	for _, kp := range cfg.KeyProviders {
		if kp.Type != "pbkdf2" {
			continue
		}
		var variable string
		switch {
		case kp.PassphraseVariable != "":
			for _, prefix := range []string{"BOG_VAR_", "TF_VAR_"} {
				if env.Getenv(prefix+kp.PassphraseVariable) != "" {
					variable = prefix + kp.PassphraseVariable
					break
				}
			}
			if variable == "" {
				results = append(results, Result{
					ID: "passphrase-available", Status: StatusWarning,
					Summary: "TF_VAR_" + kp.PassphraseVariable + " isn't set here, so plans in this shell can't read the state.",
					Fix:     "Set it from your password manager or the backup, without echoing it, or use the passphrase key provider so it comes from your keychain.",
				})
				continue
			}
		case kp.Chain != "":
			chained, ok := cfg.keyProvider(kp.Chain)
			if !ok || chained.Type != "passphrase" || chained.Args["env"] == "" || env.Getenv(chained.Args["env"]) == "" {
				continue
			}
			variable = chained.Args["env"]
		default:
			continue
		}
		if len(env.Getenv(variable)) < minimumPassphrase {
			results = append(results, Result{ID: "passphrase-available", Status: StatusProblem, Summary: variable + " is shorter than the minimum of 16 characters.", Fix: "Use a longer passphrase."})
		} else {
			results = append(results, Result{ID: "passphrase-available", Status: StatusOK, Summary: "The passphrase is set in " + variable + "."})
		}
	}
	return results
}

type lockInfo struct {
	ID        string    `json:"ID"`
	Who       string    `json:"Who"`
	Operation string    `json:"Operation"`
	Created   time.Time `json:"Created"`
}

// checkStaleLocks looks for git backend locks that have been held too long, which usually means a
// cancelled run left them behind.
func checkStaleLocks(ctx context.Context, env *Env, cfg *staticConfig) (Result, bool) {
	if cfg.Backend != "git" {
		return Result{}, false
	}
	url := cfg.BackendArgs["repository"]
	if url == "" {
		url = "origin"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := env.Git(ctx, "ls-remote", "--", url, "refs/heads/bogform-locks/*")
	if err != nil {
		return Result{}, false // The backend check reports an unreachable repository.
	}
	if out == "" {
		return Result{ID: "locks", Status: StatusOK, Summary: "The state isn't locked."}, true
	}

	// Lock details are in each lock commit's message. Fetch them into a throwaway repository, so the
	// user's checkout isn't touched.
	tmp, err := os.MkdirTemp("", "bogform-doctor-")
	if err != nil {
		return Result{}, false
	}
	defer os.RemoveAll(tmp)
	if _, err := env.Git(ctx, "init", "--quiet", "--bare", tmp); err != nil {
		return Result{}, false
	}
	if url == "origin" {
		if url, err = env.Git(ctx, "remote", "get-url", "origin"); err != nil {
			return Result{}, false
		}
	}
	if _, err := env.Git(ctx, "-C", tmp, "fetch", "--quiet", "--no-tags", "--depth=1", "--", url, "refs/heads/bogform-locks/*:refs/locks/*"); err != nil {
		return Result{}, false
	}
	refs, err := env.Git(ctx, "-C", tmp, "for-each-ref", "--format=%(objectname)", "refs/locks/")
	if err != nil {
		return Result{}, false
	}
	var stale []string
	held := 0
	for _, commit := range strings.Fields(refs) {
		held++
		message, err := env.Git(ctx, "-C", tmp, "log", "-1", "--format=%B", commit)
		if err != nil {
			continue
		}
		var info lockInfo
		if json.Unmarshal([]byte(message), &info) != nil || info.Created.IsZero() {
			continue
		}
		if age := env.Now().Sub(info.Created); age > env.StaleLockAge {
			stale = append(stale, fmt.Sprintf("%s (%s by %s, %s ago)", info.ID, info.Operation, info.Who, age.Round(time.Minute)))
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return Result{
			ID: "locks", Status: StatusWarning,
			Summary: "The state has been locked for a long time, which usually means a cancelled run left the lock: " + strings.Join(stale, "; "),
			Fix:     "Check that nothing is running, then run \"bogform force-unlock <ID>\".",
		}, true
	}
	return Result{ID: "locks", Status: StatusOK, Summary: fmt.Sprintf("The state is locked by %d run(s) that started recently.", held)}, true
}

func checkVersions(cfg *staticConfig) Result {
	var unpinned []string
	for name, pinned := range cfg.Providers {
		if !pinned {
			unpinned = append(unpinned, name)
		}
	}
	sort.Strings(unpinned)
	switch {
	case !cfg.RequiredVersion && len(unpinned) > 0:
		return Result{ID: "versions", Status: StatusWarning, Summary: "There's no required_version, and these providers have no version constraint: " + strings.Join(unpinned, ", ") + ".", Fix: "Add required_version and a version for each provider in the terraform block."}
	case !cfg.RequiredVersion:
		return Result{ID: "versions", Status: StatusWarning, Summary: "There's no required_version, so an old Bogform could run this configuration.", Fix: "Add required_version to the terraform block."}
	case len(unpinned) > 0:
		return Result{ID: "versions", Status: StatusWarning, Summary: "These providers have no version constraint: " + strings.Join(unpinned, ", ") + ".", Fix: "Add a version for each in required_providers."}
	}
	return Result{ID: "versions", Status: StatusOK, Summary: "Bogform and provider versions are constrained."}
}

func checkLockFile(ctx context.Context, env *Env) Result {
	if _, err := os.Stat(filepath.Join(env.Dir, ".terraform.lock.hcl")); err != nil {
		return Result{ID: "lock-file", Status: StatusWarning, Summary: "There's no .terraform.lock.hcl, so provider versions and checksums aren't recorded.", Fix: "Run \"bogform init\" and commit .terraform.lock.hcl."}
	}
	if _, err := env.Git(ctx, "ls-files", "--error-unmatch", ".terraform.lock.hcl"); err != nil {
		return Result{ID: "lock-file", Status: StatusWarning, Summary: ".terraform.lock.hcl isn't committed, so pipelines might use other provider versions.", Fix: "Commit it. If .gitignore excludes it, use \"git add -f\"."}
	}
	return Result{ID: "lock-file", Status: StatusOK, Summary: ".terraform.lock.hcl is committed."}
}

var (
	unpinnedPipeline = []*regexp.Regexp{
		regexp.MustCompile(`bogware/bogform@(main|master)\b`),
		regexp.MustCompile(`(?m)^\s*version:\s*latest\s*$`),
		regexp.MustCompile(`bogform:latest\b`),
		regexp.MustCompile(`(?m)^\s*bogformVersion:\s*latest\s*$`),
	}
	pipelineMarker = regexp.MustCompile(`bogware/bogform|ghcr\.io/bogware/bogform|bogform -chdir|bogformVersion`)
)

func checkPipeline(env *Env) Result {
	candidates := []string{".gitlab-ci.yml", "azure-pipelines.yml", "bitbucket-pipelines.yml"}
	workflows, _ := filepath.Glob(filepath.Join(env.Root, ".github", "workflows", "*.y*ml"))
	for _, w := range workflows {
		rel, _ := filepath.Rel(env.Root, w)
		candidates = append(candidates, rel)
	}
	var found, unpinned []string
	for _, rel := range candidates {
		content, err := os.ReadFile(filepath.Join(env.Root, rel))
		if err != nil || !pipelineMarker.Match(content) {
			continue
		}
		rel = filepath.ToSlash(rel)
		found = append(found, rel)
		for _, pattern := range unpinnedPipeline {
			if pattern.Match(content) {
				unpinned = append(unpinned, rel)
				break
			}
		}
	}
	switch {
	case len(found) == 0:
		return Result{ID: "pipeline", Status: StatusWarning, Summary: "There's no pipeline that runs Bogform, so changes aren't planned on pull requests.", Fix: "Run \"bogform ci init\" at the repository root."}
	case len(unpinned) > 0:
		return Result{ID: "pipeline", Status: StatusWarning, Summary: "These pipelines use the latest Bogform rather than a pinned version: " + strings.Join(unpinned, ", ") + ".", Fix: "Pin a release, such as bogware/bogform@v1.15.0 with version: 1.15.0, so an upgrade happens in a reviewed change."}
	}
	return Result{ID: "pipeline", Status: StatusOK, Summary: "A pipeline runs a pinned Bogform: " + strings.Join(found, ", ") + "."}
}

func checkBogformMD(env *Env) Result {
	if _, err := os.Stat(filepath.Join(env.Root, "BOGFORM.md")); err != nil {
		return Result{ID: "bogform-md", Status: StatusWarning, Summary: "There's no BOGFORM.md, which explains this setup to people and coding agents.", Fix: "Run \"bogform ci init\", which writes it, or copy templates/BOGFORM.md from bogware/bogform."}
	}
	return Result{ID: "bogform-md", Status: StatusOK, Summary: "BOGFORM.md explains the setup."}
}

func checkIgnored(ctx context.Context, env *Env) []Result {
	var results []Result
	for _, dir := range []string{".terraform", ".bogform-ci"} {
		// check-ignore takes a path inside the directory, so it works before the directory exists.
		if _, err := env.Git(ctx, "check-ignore", "-q", "--", dir+"/x"); err != nil {
			results = append(results, Result{ID: "ignored", Status: StatusWarning, Summary: dir + "/ isn't ignored by Git. It can hold credentials or saved plans.", Fix: "Add " + dir + "/ to .gitignore."})
		}
	}
	if len(results) == 0 {
		results = append(results, Result{ID: "ignored", Status: StatusOK, Summary: ".terraform/ and .bogform-ci/ are ignored by Git."})
	}
	return results
}

var (
	awsAccessKey = regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)
	privateKey   = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// Addresses on these domains are placeholders, not anyone's.
	exampleEmail = regexp.MustCompile(`@(example\.(com|org|net)|localhost)$`)
)

// checkSecrets looks for secrets and personal information in committed configuration. It reports
// where they are, never the values.
func checkSecrets(ctx context.Context, env *Env, cfg *staticConfig) []Result {
	out, err := env.Git(ctx, "ls-files", "--", "*.tf", "*.tofu", "*.bog", "*.tfvars", "*.tfvars.json")
	if err != nil {
		return nil
	}
	var problems, warnings []string
	for _, rel := range strings.Fields(out) {
		content, err := os.ReadFile(filepath.Join(env.Dir, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(content), "\n") {
			where := fmt.Sprintf("%s:%d", rel, i+1)
			switch {
			case awsAccessKey.MatchString(line):
				problems = append(problems, where+" (an AWS access key)")
			case privateKey.MatchString(line):
				problems = append(problems, where+" (a private key)")
			case strings.HasSuffix(rel, ".tfvars") || strings.HasSuffix(rel, ".tfvars.json"):
				if email := emailAddress.FindString(line); email != "" && !exampleEmail.MatchString(email) {
					warnings = append(warnings, where+" (an email address)")
				}
			}
		}
	}
	for name, def := range cfg.VariableDefaults {
		if email := emailAddress.FindString(def.Value); email != "" && !exampleEmail.MatchString(email) {
			warnings = append(warnings, fmt.Sprintf("%s:%d (the default of var.%s is an email address)", def.File, def.Line, name))
		}
	}
	sort.Strings(problems)
	sort.Strings(warnings)

	var results []Result
	if len(problems) > 0 {
		results = append(results, Result{
			ID: "secrets", Status: StatusProblem,
			Summary: "Committed files look like they contain secrets: " + strings.Join(problems, "; ") + ".",
			Fix:     "Revoke or rotate them now, since they're in Git's history. Then pass them in from pipeline secrets as sensitive variables.",
		})
	}
	if len(warnings) > 0 {
		results = append(results, Result{
			ID: "personal-information", Status: StatusWarning,
			Summary: "Committed files contain personal information, which plans show in pull requests: " + strings.Join(warnings, "; ") + ".",
			Fix:     "Make the variable sensitive with no default, and set it from a pipeline secret.",
		})
	}
	if len(results) == 0 {
		results = append(results, Result{ID: "secrets", Status: StatusOK, Summary: "No secrets or email addresses were found in committed configuration."})
	}
	return results
}
