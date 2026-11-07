// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package e2etest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/bogware/bogform/internal/ci"
)

const ciConfig = `
variable "name" {
  type    = string
  default = "first"
}

variable "secret" {
  type      = string
  default   = "hunter2"
  sensitive = true
}

resource "terraform_data" "example" {
  input = var.name
}

resource "terraform_data" "sensitive" {
  input = var.secret
}

output "name" {
  value = terraform_data.example.output
}
`

// ciServer is a fake of GitHub's issue comment API.
type ciServer struct {
	mu       sync.Mutex
	comments map[int]string
	next     int
	tokens   []string
}

func (s *ciServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, r.Header.Get("Authorization"))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		w.WriteHeader(http.StatusForbidden) // As with the token that GitHub Actions provides.
	case r.Method == http.MethodGet:
		var out []map[string]any
		for id, body := range s.comments {
			out = append(out, map[string]any{"id": id, "body": body, "user": map[string]string{"login": "github-actions[bot]"}})
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost:
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.next++
		s.comments[s.next] = in["body"]
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPatch:
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		match := regexp.MustCompile(`/(\d+)$`).FindStringSubmatch(r.URL.Path)
		var id int
		_ = json.Unmarshal([]byte(match[1]), &id)
		s.comments[id] = in["body"]
	}
}

type ciResult struct {
	exit   int
	stdout string
	stderr string
}

func runCI(t *testing.T, dir string, env []string, args ...string) ciResult {
	t.Helper()
	cmd := exec.Command(tofuBin, append([]string{"ci"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := ciResult{stdout: stdout.String(), stderr: stderr.String()}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result
}

func ciEnvironment(t *testing.T, apiURL, event string) ([]string, string) {
	t.Helper()
	eventPath := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(eventPath, []byte(`{"pull_request":{"number":12}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	summary := filepath.Join(t.TempDir(), "summary.md")
	return []string{
		"GITHUB_ACTIONS=true",
		"GITHUB_EVENT_NAME=" + event,
		"GITHUB_EVENT_PATH=" + eventPath,
		"GITHUB_REPOSITORY=acme/infra",
		"GITHUB_API_URL=" + apiURL,
		"GITHUB_SERVER_URL=https://github.example",
		"GITHUB_RUN_ID=99",
		"GITHUB_SHA=0123456789abcdef",
		"GITHUB_HEAD_REF=feature",
		"GITHUB_REF_NAME=main",
		"GITHUB_STEP_SUMMARY=" + summary,
		"BOG_CI_TOKEN=token-value",
		"BOG_CI_TEST_SENTINEL=1",
	}, summary
}

func writeCIConfig(t *testing.T, dir, config string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readReport(t *testing.T, dir string) ci.Report {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ci.WorkDir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report ci.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestCI_pullRequestWorkflow(t *testing.T) {
	t.Parallel()
	server := &ciServer{comments: map[int]string{}}
	api := httptest.NewServer(server)
	defer api.Close()

	dir := t.TempDir()
	writeCIConfig(t, dir, ciConfig)
	env, summary := ciEnvironment(t, api.URL, "pull_request")

	// The first plan creates a comment.
	result := runCI(t, dir, env, "plan")
	if result.exit != 0 {
		t.Fatalf("plan failed: %s\n%s", result.stdout, result.stderr)
	}
	if len(server.comments) != 1 {
		t.Fatalf("expected one comment: %v", server.comments)
	}
	comment := server.comments[1]
	for _, want := range []string{"Changes planned", "`terraform_data.example`", "➕ create", "**2** to add", "https://github.example/acme/infra/actions/runs/99", "`01234567`"} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment is missing %q:\n%s", want, comment)
		}
	}
	if strings.Contains(comment, "hunter2") {
		t.Errorf("the comment leaks a sensitive value:\n%s", comment)
	}
	if server.tokens[0] != "Bearer token-value" {
		t.Errorf("wrong token: %q", server.tokens[0])
	}

	report := readReport(t, dir)
	if report.Status != ci.StatusChanges || report.Counts.Add != 2 || report.Run.PullRequest != "12" {
		t.Errorf("wrong report: %+v", report)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, ci.WorkDir, "report.json"))
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "token-value") {
		t.Errorf("the report leaks a secret:\n%s", raw)
	}

	content, err := os.ReadFile(summary)
	if err != nil || !strings.Contains(string(content), "Changes planned") {
		t.Errorf("job summary wasn't written: %v\n%s", err, content)
	}

	// A second plan updates the same comment instead of adding another.
	result = runCI(t, dir, env, "plan", "-var=name=second")
	if result.exit != 0 {
		t.Fatalf("plan failed: %s\n%s", result.stdout, result.stderr)
	}
	if len(server.comments) != 1 {
		t.Fatalf("expected the comment to be updated: %v", server.comments)
	}

	// Applying from a pull request is refused, without running anything.
	result = runCI(t, dir, env, "apply")
	if result.exit != 1 || !strings.Contains(result.stderr, "Refusing to apply") {
		t.Fatalf("apply from a pull request should be refused: %d\n%s\n%s", result.exit, result.stdout, result.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); err == nil {
		t.Fatal("the refused apply changed state")
	}
}

func TestCI_applyThenDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeCIConfig(t, dir, ciConfig)
	env, _ := ciEnvironment(t, "http://127.0.0.1:1", "push")

	result := runCI(t, dir, env, "apply")
	if result.exit != 0 {
		t.Fatalf("apply failed: %s\n%s", result.stdout, result.stderr)
	}
	report := readReport(t, dir)
	if report.Status != ci.StatusApplied || report.Counts.Add != 2 {
		t.Fatalf("wrong report: %+v", report)
	}

	// With everything applied, there's nothing to do.
	result = runCI(t, dir, env, "apply")
	if result.exit != 0 {
		t.Fatalf("second apply failed: %s\n%s", result.stdout, result.stderr)
	}
	if report := readReport(t, dir); report.Status != ci.StatusNoChanges {
		t.Fatalf("expected no changes: %+v", report)
	}

	result = runCI(t, dir, env, "drift")
	if result.exit != 0 {
		t.Fatalf("expected no drift: %d\n%s\n%s", result.exit, result.stdout, result.stderr)
	}
	if report := readReport(t, dir); report.Kind != ci.KindDrift || report.Status != ci.StatusNoChanges {
		t.Fatalf("wrong report: %+v", report)
	}
}

func TestCI_failureIsReported(t *testing.T) {
	t.Parallel()
	server := &ciServer{comments: map[int]string{}}
	api := httptest.NewServer(server)
	defer api.Close()

	dir := t.TempDir()
	writeCIConfig(t, dir, `resource "terraform_data" "broken" { input = var.missing }`)
	env, _ := ciEnvironment(t, api.URL, "pull_request")

	result := runCI(t, dir, env, "plan")
	if result.exit != 1 {
		t.Fatalf("expected a failure: %d", result.exit)
	}
	if !strings.Contains(server.comments[1], "Failed") || !strings.Contains(server.comments[1], "var.missing") {
		t.Fatalf("the comment should explain the failure:\n%s", server.comments[1])
	}
	if report := readReport(t, dir); report.Status != ci.StatusFailed {
		t.Fatalf("wrong report: %+v", report)
	}
}

func TestCI_commentFailureDoesNotFailTheRun(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer api.Close()

	dir := t.TempDir()
	writeCIConfig(t, dir, ciConfig)
	env, _ := ciEnvironment(t, api.URL, "pull_request")

	result := runCI(t, dir, env, "plan")
	if result.exit != 0 {
		t.Fatalf("a failed comment shouldn't fail the run: %s\n%s", result.stdout, result.stderr)
	}
	if !strings.Contains(result.stderr, "Warning: couldn't comment") {
		t.Fatalf("expected a warning: %s", result.stderr)
	}
}

func TestCI_tokenIsHiddenFromConfiguration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Anything that the configuration can read, it could also send away.
	writeCIConfig(t, dir, `
output "visible" { value = "${try(file("/proc/self/environ"), "")}" }
`)
	env, _ := ciEnvironment(t, "http://127.0.0.1:1", "push")
	result := runCI(t, dir, env, "plan", "-no-comment")
	if strings.Contains(result.stdout, "token-value") {
		t.Fatalf("the token reached the plan's environment:\n%s", result.stdout)
	}
}

func TestCI_workspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeCIConfig(t, dir, ciConfig)
	env, _ := ciEnvironment(t, "http://127.0.0.1:1", "push")

	result := runCI(t, dir, env, "apply", "-workspace=staging")
	if result.exit != 0 {
		t.Fatalf("apply failed: %s\n%s", result.stdout, result.stderr)
	}
	if report := readReport(t, dir); report.Workspace != "staging" {
		t.Fatalf("wrong workspace: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate.d", "staging", "terraform.tfstate")); err != nil {
		t.Fatal(err)
	}
}

func TestCI_init(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"github", "gitlab", "azure", "bitbucket"} {
		dir := t.TempDir()
		result := runCI(t, dir, nil, "init", platform)
		if result.exit != 0 || !strings.Contains(result.stdout, "Next steps") {
			t.Fatalf("%s: %d\n%s\n%s", platform, result.exit, result.stdout, result.stderr)
		}
	}
	if result := runCI(t, t.TempDir(), nil, "init", "subversion"); result.exit == 0 {
		t.Fatal("an unknown platform should fail")
	}
}

// TestCI_gitBackendWithEncryption checks the whole flow: encrypted state, on its own branch, with the
// real CLI.
func TestCI_gitBackendWithEncryption(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't available")
	}
	origin := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", origin).CombinedOutput(); err != nil {
		t.Fatalf("%s: %s", err, out)
	}
	originSlash := filepath.ToSlash(origin)

	unencrypted := `terraform {
  backend "git" {
    repository = "` + originSlash + `"
  }
}
` + ciConfig
	dir := t.TempDir()
	writeCIConfig(t, dir, unencrypted)
	env, _ := ciEnvironment(t, "http://127.0.0.1:1", "push")

	// Without encryption, the backend refuses to store state.
	result := runCI(t, dir, env, "apply")
	if result.exit == 0 || !strings.Contains(result.stdout+result.stderr, "refused to store unencrypted state") {
		t.Fatalf("expected a refusal: %d\n%s\n%s", result.exit, result.stdout, result.stderr)
	}

	encrypted := `terraform {
  backend "git" {
    repository = "` + originSlash + `"
  }
  encryption {
    key_provider "pbkdf2" "key" {
      passphrase = "correct horse battery staple"
    }
    method "aes_gcm" "method" {
      keys = key_provider.pbkdf2.key
    }
    state {
      method = method.aes_gcm.method
    }
    plan {
      method = method.aes_gcm.method
    }
  }
}
` + ciConfig
	dir = t.TempDir()
	writeCIConfig(t, dir, encrypted)
	result = runCI(t, dir, env, "apply")
	if result.exit != 0 {
		t.Fatalf("apply failed: %s\n%s", result.stdout, result.stderr)
	}

	stored, err := exec.Command("git", "--git-dir", origin, "show", "bogform-state:terraform.tfstate").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "encrypted_data") || strings.Contains(string(stored), "hunter2") {
		t.Fatalf("state isn't encrypted:\n%s", stored)
	}
	refs, err := exec.Command("git", "--git-dir", origin, "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(refs), "bogform-locks") {
		t.Fatalf("the lock should have been released:\n%s", refs)
	}

	// A fresh checkout can read the state back: there's nothing left to change.
	dir = t.TempDir()
	writeCIConfig(t, dir, encrypted)
	result = runCI(t, dir, env, "apply")
	if result.exit != 0 {
		t.Fatalf("second apply failed: %s\n%s", result.stdout, result.stderr)
	}
	if report := readReport(t, dir); report.Status != ci.StatusNoChanges {
		t.Fatalf("expected no changes from a fresh checkout: %+v", report)
	}
}
