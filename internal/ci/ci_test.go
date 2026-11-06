// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func envMap(values map[string]string) EnvFunc {
	return func(name string) string { return values[name] }
}

func TestDetect(t *testing.T) {
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"number":42,"head":{"repo":{"fork":true}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		env  map[string]string
		want Context
	}{
		"local": {env: nil, want: Context{Platform: PlatformNone}},
		"github pull request": {
			env: map[string]string{
				"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "pull_request", "GITHUB_EVENT_PATH": event,
				"GITHUB_REPOSITORY": "acme/infra", "GITHUB_SHA": "abc", "GITHUB_HEAD_REF": "feature",
				"GITHUB_SERVER_URL": "https://github.com", "GITHUB_RUN_ID": "7",
			},
			want: Context{Platform: PlatformGitHub, Repository: "acme/infra", Commit: "abc", Branch: "feature",
				IsPullRequest: true, PullRequest: "42", FromFork: true, RunURL: "https://github.com/acme/infra/actions/runs/7"},
		},
		"github push": {
			env:  map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_EVENT_NAME": "push", "GITHUB_REF_NAME": "main"},
			want: Context{Platform: PlatformGitHub, Branch: "main"},
		},
		"gitlab merge request": {
			env: map[string]string{
				"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "5", "CI_PROJECT_PATH": "acme/infra",
				"CI_MERGE_REQUEST_SOURCE_BRANCH_NAME": "feature", "CI_JOB_URL": "https://gitlab.com/j/1",
			},
			want: Context{Platform: PlatformGitLab, Repository: "acme/infra", Branch: "feature", IsPullRequest: true, PullRequest: "5", RunURL: "https://gitlab.com/j/1"},
		},
		"azure pull request": {
			env: map[string]string{
				"TF_BUILD": "True", "SYSTEM_COLLECTIONURI": "https://dev.azure.com/acme", "SYSTEM_TEAMPROJECT": "infra",
				"BUILD_BUILDID": "9", "SYSTEM_PULLREQUEST_PULLREQUESTID": "3", "SYSTEM_PULLREQUEST_SOURCEBRANCH": "refs/heads/feature",
			},
			want: Context{Platform: PlatformAzure, Branch: "feature", IsPullRequest: true, PullRequest: "3", RunURL: "https://dev.azure.com/acme/infra/_build/results?buildId=9"},
		},
		"bitbucket pull request": {
			env:  map[string]string{"BITBUCKET_BUILD_NUMBER": "2", "BITBUCKET_REPO_FULL_NAME": "acme/infra", "BITBUCKET_PR_ID": "8", "BITBUCKET_BRANCH": "feature"},
			want: Context{Platform: PlatformBitbucket, Repository: "acme/infra", Branch: "feature", IsPullRequest: true, PullRequest: "8"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := Detect(envMap(test.env))
			got.apiURL, got.projectURL, got.summaryPath = "", "", ""
			if got != test.want {
				t.Fatalf("wrong context\ngot:  %+v\nwant: %+v", got, test.want)
			}
		})
	}
}

// fakeForge records comments the way each platform's API does.
type fakeForge struct {
	mu       sync.Mutex
	comments map[int64]string
	nextID   int64
	failures int
	token    string
}

func (f *fakeForge) add(body string) int64 {
	f.nextID++
	f.comments[f.nextID] = body
	return f.nextID
}

func (f *fakeForge) handler(t *testing.T, platform string) http.Handler {
	t.Helper()
	decode := func(r *http.Request) map[string]any {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		return in
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failures > 0 {
			f.failures--
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		f.token = r.Header.Get("Authorization") + r.Header.Get("PRIVATE-TOKEN")

		id := int64(0)
		if m := regexp.MustCompile(`/(\d+)$`).FindStringSubmatch(r.URL.Path); m != nil {
			json.Unmarshal([]byte(m[1]), &id)
		}
		switch platform {
		case PlatformGitHub, PlatformGitLab:
			switch r.Method {
			case http.MethodGet:
				out := []map[string]any{}
				for commentID, body := range f.comments {
					out = append(out, map[string]any{"id": commentID, "body": body})
				}
				json.NewEncoder(w).Encode(out)
			case http.MethodPost:
				f.add(decode(r)["body"].(string))
			case http.MethodPatch, http.MethodPut:
				f.comments[id] = decode(r)["body"].(string)
			}
		case PlatformAzure:
			switch r.Method {
			case http.MethodGet:
				threads := []map[string]any{}
				for commentID, body := range f.comments {
					threads = append(threads, map[string]any{"id": 900, "comments": []map[string]any{{"id": commentID, "content": body}}})
				}
				json.NewEncoder(w).Encode(map[string]any{"value": threads})
			case http.MethodPost:
				comment := decode(r)["comments"].([]any)[0].(map[string]any)
				f.add(comment["content"].(string))
			case http.MethodPatch:
				f.comments[id] = decode(r)["content"].(string)
			}
		case PlatformBitbucket:
			switch r.Method {
			case http.MethodGet:
				values := []map[string]any{}
				for commentID, body := range f.comments {
					values = append(values, map[string]any{"id": commentID, "content": map[string]string{"raw": body}})
				}
				json.NewEncoder(w).Encode(map[string]any{"values": values})
			case http.MethodPost:
				f.add(decode(r)["content"].(map[string]any)["raw"].(string))
			case http.MethodPut:
				f.comments[id] = decode(r)["content"].(map[string]any)["raw"].(string)
			}
		}
	})
}

func TestCommentersCreateThenUpdate(t *testing.T) {
	for _, platform := range []string{PlatformGitHub, PlatformGitLab, PlatformAzure, PlatformBitbucket} {
		t.Run(platform, func(t *testing.T) {
			forge := &fakeForge{comments: map[int64]string{}}
			forge.add("someone else's comment")
			server := httptest.NewServer(forge.handler(t, platform))
			defer server.Close()

			run := Context{Platform: platform, IsPullRequest: true, PullRequest: "1", Repository: "acme/infra", apiURL: server.URL, projectURL: "proj"}
			if platform == PlatformAzure {
				run.apiURL = server.URL + "/"
				run.projectURL = "proj/repo"
			}
			commenter, err := NewCommenter(run, "secret", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			setRetry(commenter)

			marker := Marker(KindPlan, ".", "default")
			if err := commenter.Upsert(t.Context(), marker, marker+"\nfirst"); err != nil {
				t.Fatal(err)
			}
			if err := commenter.Upsert(t.Context(), marker, marker+"\nsecond"); err != nil {
				t.Fatal(err)
			}

			if len(forge.comments) != 2 {
				t.Fatalf("expected the comment to be updated in place, got %v", forge.comments)
			}
			if forge.comments[1] != "someone else's comment" {
				t.Fatalf("another comment was changed: %q", forge.comments[1])
			}
			if !strings.HasSuffix(forge.comments[2], "second") {
				t.Fatalf("comment wasn't updated: %q", forge.comments[2])
			}
			if !strings.Contains(forge.token, "secret") {
				t.Fatalf("token wasn't sent: %q", forge.token)
			}
		})
	}
}

func setRetry(c Commenter) {
	switch v := c.(type) {
	case *githubCommenter:
		v.api.retryDelay = 1
	case *gitlabCommenter:
		v.api.retryDelay = 1
	case *azureCommenter:
		v.api.retryDelay = 1
	case *bitbucketCommenter:
		v.api.retryDelay = 1
	}
}

func TestCommenterRetriesTemporaryFailures(t *testing.T) {
	forge := &fakeForge{comments: map[int64]string{}, failures: 2}
	server := httptest.NewServer(forge.handler(t, PlatformGitHub))
	defer server.Close()
	commenter, err := NewCommenter(Context{Platform: PlatformGitHub, IsPullRequest: true, PullRequest: "1", Repository: "a/b", apiURL: server.URL}, "t", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	setRetry(commenter)
	if err := commenter.Upsert(t.Context(), "m", "m body"); err != nil {
		t.Fatal(err)
	}
	if len(forge.comments) != 1 {
		t.Fatalf("comment wasn't created: %v", forge.comments)
	}
}

func TestNewCommenter_missingPrerequisites(t *testing.T) {
	if _, err := NewCommenter(Context{Platform: PlatformGitHub}, "t", nil); !errors.Is(err, ErrNoCommenter) {
		t.Fatalf("outside a pull request: %v", err)
	}
	if _, err := NewCommenter(Context{Platform: PlatformGitHub, IsPullRequest: true, PullRequest: "1"}, "", nil); err == nil {
		t.Fatal("expected an error without a token")
	}
}

func TestSummarizePlan(t *testing.T) {
	raw := `{
	  "resource_changes": [
	    {"address":"a.create","change":{"actions":["create"],"after":{"password":"hunter2"}}},
	    {"address":"a.update","change":{"actions":["update"]}},
	    {"address":"a.delete","change":{"actions":["delete"]}},
	    {"address":"a.replace","change":{"actions":["delete","create"]}},
	    {"address":"a.same","change":{"actions":["no-op"]}},
	    {"address":"a.import","change":{"actions":["no-op"],"importing":{"id":"x"}}},
	    {"address":"data.a.read","change":{"actions":["read"]}}
	  ],
	  "output_changes": {"b":{"actions":["create"]},"a":{"actions":["update"]},"c":{"actions":["no-op"]}}
	}`
	var report Report
	if err := report.summarizePlan([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	want := Counts{Add: 1, Change: 1, Destroy: 1, Replace: 1, Import: 1, Read: 1}
	if report.Counts != want {
		t.Fatalf("wrong counts: %+v", report.Counts)
	}
	if len(report.Changes) != 6 {
		t.Fatalf("wrong changes: %+v", report.Changes)
	}
	if strings.Join(report.OutputsChanged, ",") != "a,b" {
		t.Fatalf("wrong outputs: %v", report.OutputsChanged)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "hunter2") {
		t.Fatal("the report must not include attribute values")
	}
}

func TestRender(t *testing.T) {
	report := &Report{
		Kind: KindPlan, Status: StatusChanges, Directory: "infra", Workspace: "prod", BogformVersion: "1.0.0",
		Run:     Context{Commit: "0123456789abcdef", RunURL: "https://example.com/run/1"},
		Counts:  Counts{Add: 1},
		Changes: []Change{{Address: "aws_s3_bucket.a", Actions: []string{"create"}}},
		Output:  "Plan: 1 to add",
	}
	got := report.Render(RenderOptions{Collapsible: true})
	for _, want := range []string{Marker(KindPlan, "infra", "prod"), "Changes planned", "`aws_s3_bucket.a`", "`01234567`", "<details>", "Plan: 1 to add", "View this run"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if plain := report.Render(RenderOptions{}); strings.Contains(plain, "<details>") {
		t.Errorf("should not use details:\n%s", plain)
	}
	if hidden := report.Render(RenderOptions{HideOutput: true}); strings.Contains(hidden, "Plan: 1 to add") {
		t.Errorf("should hide output:\n%s", hidden)
	}
}

func TestRender_cannotBreakOutOfCode(t *testing.T) {
	report := &Report{
		Kind: KindPlan, Status: StatusChanges, Directory: ".", Workspace: "default",
		Changes: []Change{{Address: "a.b[\"x` | <img src=x>\"]", Actions: []string{"create"}}},
		Output:  "before\n```\n# injected heading\n```\nafter",
	}
	got := report.Render(RenderOptions{})
	if strings.Contains(got, "\n```\n# injected") && !strings.Contains(got, "````\nbefore") {
		t.Fatalf("output can escape its code block:\n%s", got)
	}
	if !strings.Contains(got, "\\|") {
		t.Fatalf("pipe should be escaped so it can't break the table:\n%s", got)
	}
	if !strings.Contains(got, "`` a.b") {
		t.Fatalf("address with a backtick should use a longer delimiter:\n%s", got)
	}
}

func TestRender_respectsLimit(t *testing.T) {
	report := &Report{Kind: KindPlan, Status: StatusChanges, Directory: ".", Workspace: "default", Output: "START\n" + strings.Repeat("x", 100000) + "\nEND"}
	got := report.Render(RenderOptions{MaxBytes: 32000})
	if len(got) > 32000 {
		t.Fatalf("result is %d bytes", len(got))
	}
	for _, want := range []string{"START", "END", "output truncated"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestRenderChanges_limitsLongLists(t *testing.T) {
	changes := make([]Change, maxListedChanges+5)
	for i := range changes {
		changes[i] = Change{Address: "a.b", Actions: []string{"create"}}
	}
	if got := renderChanges(changes); !strings.Contains(got, "...and 5 more") {
		t.Fatalf("list wasn't limited:\n%s", got)
	}
}

func TestMarker_isUniquePerDirectoryAndWorkspace(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range []string{Marker(KindPlan, ".", "default"), Marker(KindPlan, "a", "default"), Marker(KindPlan, ".", "prod"), Marker(KindApply, ".", "default")} {
		if seen[m] {
			t.Fatalf("duplicate marker %s", m)
		}
		seen[m] = true
	}
}

func TestRenderPipeline(t *testing.T) {
	for _, platform := range []string{PlatformGitHub, PlatformGitLab, PlatformAzure, PlatformBitbucket} {
		t.Run(platform, func(t *testing.T) {
			content, err := RenderPipeline(ScaffoldOptions{Platform: platform, Version: "1.2.3", DefaultBranch: "trunk", Directory: "infra/prod", Environment: "live"})
			if err != nil {
				t.Fatal(err)
			}
			var parsed map[string]any
			if err := yaml.Unmarshal(content, &parsed); err != nil {
				t.Fatalf("invalid YAML: %s\n%s", err, content)
			}
			for _, want := range []string{"1.2.3", "trunk", "infra/prod", "live", "plan", "apply"} {
				if !strings.Contains(string(content), want) {
					t.Errorf("missing %q in:\n%s", want, content)
				}
			}
			if strings.Contains(string(content), "[[") || strings.Contains(string(content), "<no value>") {
				t.Errorf("unrendered template values in:\n%s", content)
			}
		})
	}
}

func TestRenderPipeline_noAnchorsOnAzure(t *testing.T) {
	// Azure Pipelines doesn't support YAML anchors and aliases.
	content, err := RenderPipeline(ScaffoldOptions{Platform: PlatformAzure})
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?m)(:\s|-\s)[&*][A-Za-z]`).Match(content) {
		t.Fatalf("Azure Pipelines doesn't support anchors:\n%s", content)
	}
}

func TestRenderPipeline_devVersionUsesLatest(t *testing.T) {
	content, err := RenderPipeline(ScaffoldOptions{Platform: PlatformGitLab, Version: "1.14.0-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "bogform:latest") {
		t.Fatalf("a development version has no image to pin:\n%s", content)
	}
}

func TestRenderPipeline_rejectsUnsafeValues(t *testing.T) {
	for _, opts := range []ScaffoldOptions{
		{Platform: "unknown"},
		{Platform: PlatformGitHub, DefaultBranch: "main\n  evil: true"},
		{Platform: PlatformGitHub, Directory: "$(curl evil)"},
		{Platform: PlatformGitLab, Environment: "a b"},
	} {
		if _, err := RenderPipeline(opts); err == nil {
			t.Errorf("expected an error for %+v", opts)
		}
	}
}

func TestScaffold(t *testing.T) {
	root := t.TempDir()
	opts := ScaffoldOptions{Platform: PlatformGitHub, Root: root}
	path, err := Scaffold(opts)
	if err != nil {
		t.Fatal(err)
	}
	if path != ".github/workflows/bogform.yml" {
		t.Fatalf("wrong path %q", path)
	}
	if _, err := os.Stat(filepath.Join(root, ".github", "workflows", "bogform.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Scaffold(opts); err == nil {
		t.Fatal("should not replace an existing file without -force")
	}
	opts.Force = true
	if _, err := Scaffold(opts); err != nil {
		t.Fatal(err)
	}
}

func TestDetectPlatformFromRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"git@github.com:acme/infra.git":              PlatformGitHub,
		"https://gitlab.com/acme/infra.git":          PlatformGitLab,
		"https://dev.azure.com/acme/infra/_git/repo": PlatformAzure,
		"git@bitbucket.org:acme/infra.git":           PlatformBitbucket,
		"https://example.com/infra.git":              "",
	} {
		if got := DetectPlatformFromRemote(remote); got != want {
			t.Errorf("%s: got %q, want %q", remote, got, want)
		}
	}
}

func TestNewRunner_keepsTokenFromChildren(t *testing.T) {
	runner := NewRunner(Options{
		Env:    []string{"BOG_CI_TOKEN=secret", "TF_CI_TOKEN=secret", "PATH=/bin", "GITHUB_ACTIONS=true"},
		Stdout: io.Discard, Stderr: io.Discard,
	})
	if runner.token != "secret" {
		t.Fatalf("token not read: %q", runner.token)
	}
	for _, entry := range runner.env {
		if strings.Contains(entry, "secret") {
			t.Fatalf("token would reach child processes: %v", runner.env)
		}
	}
	if !strings.Contains(strings.Join(runner.env, " "), "TF_IN_AUTOMATION=1") {
		t.Fatalf("automation mode not set: %v", runner.env)
	}
}

func TestApply_refusesPullRequests(t *testing.T) {
	t.Chdir(t.TempDir())
	event := filepath.Join(t.TempDir(), "event.json")
	os.WriteFile(event, []byte(`{"pull_request":{"number":1}}`), 0o600)
	var stderr strings.Builder
	runner := NewRunner(Options{
		Binary: "binary-that-must-not-run",
		Env:    []string{"GITHUB_ACTIONS=true", "GITHUB_EVENT_NAME=pull_request", "GITHUB_EVENT_PATH=" + event},
		Stdout: io.Discard, Stderr: &stderr, NoComment: true,
	})
	if code := runner.Apply(context.Background()); code != ExitFailed {
		t.Fatalf("wrong exit code %d", code)
	}
	if !strings.Contains(stderr.String(), "Refusing to apply from a pull request") {
		t.Fatalf("wrong message: %s", stderr.String())
	}
}
