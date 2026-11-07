// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

func writeReport(t *testing.T, dir string, report ci.Report) {
	t.Helper()
	path := filepath.Join(dir, ".bogform-ci", "report.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func localReport(kind, status, directory string, finished time.Time) ci.Report {
	return ci.Report{
		FormatVersion: ci.ReportFormatVersion, Kind: kind, Status: status, Directory: directory, Workspace: "default",
		Counts:    ci.Counts{Add: 1},
		Changes:   []ci.Change{{Address: "aws_s3_bucket.a", Actions: []string{"create"}}},
		StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
	}
}

const testToken = "s3cret-token"

func testLocal(t *testing.T) (*Local, string) {
	t.Helper()
	root := t.TempDir()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	writeReport(t, filepath.Join(root, "network"), localReport(ci.KindPlan, ci.StatusChanges, "network", now.Add(-time.Hour)))
	failed := localReport(ci.KindApply, ci.StatusFailed, "dns", now.Add(-2*time.Hour))
	failed.Error = "Error: access denied"
	failed.Run = ci.Context{RunURL: "javascript:alert(1)"}
	writeReport(t, filepath.Join(root, "dns"), failed)
	pipeline := localReport(ci.KindPlan, ci.StatusNoChanges, "infra", now.Add(-3*time.Hour))
	pipeline.Run = ci.Context{Platform: ci.PlatformGitHub, Repository: "acme/site", IsPullRequest: true, PullRequest: "9", RunURL: "https://github.com/acme/site/actions/runs/1"}
	writeReport(t, filepath.Join(root, "site"), pipeline)
	// These aren't searched.
	writeReport(t, filepath.Join(root, "node_modules", "pkg"), localReport(ci.KindPlan, ci.StatusFailed, "ignored", now))
	writeReport(t, filepath.Join(root, "network", ".terraform", "modules", "m"), localReport(ci.KindPlan, ci.StatusFailed, "ignored", now))
	// A broken file doesn't hide the others.
	if err := os.MkdirAll(filepath.Join(root, "broken", ".bogform-ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "broken", ".bogform-ci", "report.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewLocal([]string{root}, testToken, nil)
	l.now = func() time.Time { return now }
	l.repository = func(string) string { return "acme/infra" }
	return l, root
}

func get(t *testing.T, handler http.Handler, target string, cookie bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = "127.0.0.1:4321"
	if cookie {
		r.AddCookie(&http.Cookie{Name: "bogform_ui_4321", Value: testToken})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestFindReports(t *testing.T) {
	_, root := testLocal(t)
	reports, problems := FindReports([]string{root, root})
	if len(reports) != 3 {
		t.Errorf("found %d reports, want 3", len(reports))
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "isn't a valid report") {
		t.Errorf("unexpected problems: %v", problems)
	}
}

func TestLocal_access(t *testing.T) {
	l, _ := testLocal(t)
	handler := l.Handler()

	if w := get(t, handler, "/api/v1/overview", false); w.Code != http.StatusUnauthorized {
		t.Errorf("without the token: %d", w.Code)
	}
	if w := get(t, handler, "/?token=wrong", false); w.Code != http.StatusUnauthorized {
		t.Errorf("with the wrong token: %d", w.Code)
	}

	w := get(t, handler, "/workspaces?token="+testToken+"&x=1", false)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/workspaces?x=1" {
		t.Fatalf("the token should be swapped for a cookie: %d %q", w.Code, w.Header().Get("Location"))
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "bogform_ui_4321" || cookie.Value != testToken || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("unexpected cookie: %+v", cookie)
	}

	r := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	r.Host = "attacker.example:4321"
	r.AddCookie(&http.Cookie{Name: "bogform_ui_4321", Value: testToken})
	rebound := httptest.NewRecorder()
	handler.ServeHTTP(rebound, r)
	if rebound.Code != http.StatusForbidden {
		t.Errorf("a request for another host name must be refused: %d", rebound.Code)
	}

	post := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/1/runs", nil)
	post.Host = "127.0.0.1:4321"
	post.AddCookie(&http.Cookie{Name: "bogform_ui_4321", Value: testToken})
	refused := httptest.NewRecorder()
	handler.ServeHTTP(refused, post)
	if refused.Code < 400 {
		t.Errorf("starting a run must not be possible: %d", refused.Code)
	}
	if got := get(t, handler, "/api/v1/me", true).Body.String(); !strings.Contains(got, `"local":true`) {
		t.Errorf("unexpected /me: %s", got)
	}
}

func TestLocal_overview(t *testing.T) {
	l, _ := testLocal(t)
	w := get(t, l.Handler(), "/api/v1/overview", true)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var overview struct {
		Workspaces   int                  `json:"workspaces"`
		Failing      int                  `json:"failing"`
		RunsToday    int                  `json:"runs_today"`
		RecentRuns   []Run                `json:"recent_runs"`
		Repositories []RepositoryOverview `json:"repositories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Workspaces != 3 || overview.Failing != 1 || overview.RunsToday != 3 || len(overview.RecentRuns) != 3 {
		t.Fatalf("unexpected overview: %s", w.Body)
	}
	if len(overview.RecentRuns[0].Changes) != 0 {
		t.Error("lists shouldn't include each run's changes")
	}
	infra := overview.Repositories[0]
	if infra.Repository != "acme/infra" || infra.Platform != "local" || len(infra.Workspaces) != 2 || infra.Workspaces[0].Directory != "dns" {
		t.Fatalf("the repository with a failure should come first, failure first: %+v", overview.Repositories)
	}
	if network := infra.Workspaces[1]; network.OpenChanges == nil || network.LastPlan == nil {
		t.Errorf("network has an unapplied plan: %+v", network)
	}
	if site := overview.Repositories[1]; site.Repository != "acme/site" || site.Platform != ci.PlatformGitHub || site.Workspaces[0].LastPlan.PullRequest != "9" {
		t.Errorf("a report from a pipeline keeps its repository and pull request: %+v", site)
	}
}

func TestLocal_details(t *testing.T) {
	l, _ := testLocal(t)
	handler := l.Handler()
	var list struct{ Workspaces []Workspace }
	if err := json.Unmarshal(get(t, handler, "/api/v1/workspaces", true).Body.Bytes(), &list); err != nil || len(list.Workspaces) != 3 {
		t.Fatalf("unexpected workspaces: %v %+v", err, list)
	}
	var dns Workspace
	for _, workspace := range list.Workspaces {
		if workspace.Directory == "dns" {
			dns = workspace
		}
	}

	again := l.load(mustFind(t, l))
	if again.workspaces[dns.ID] == nil {
		t.Error("IDs must stay the same between scans")
	}

	detail := get(t, handler, "/api/v1/workspaces/"+itoa(dns.ID), true).Body.String()
	if !strings.Contains(detail, `"can_start_run":false`) || !strings.Contains(detail, `"local":true`) {
		t.Errorf("unexpected workspace: %s", detail)
	}
	var runs struct{ Runs []Run }
	if err := json.Unmarshal(get(t, handler, "/api/v1/workspaces/"+itoa(dns.ID)+"/runs", true).Body.Bytes(), &runs); err != nil || len(runs.Runs) != 1 {
		t.Fatalf("unexpected runs: %v %+v", err, runs)
	}
	var run Run
	if err := json.Unmarshal(get(t, handler, "/api/v1/runs/"+itoa(runs.Runs[0].ID), true).Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Error != "Error: access denied" || len(run.Changes) != 1 || run.RunURL != "" {
		t.Errorf("unexpected run, which mustn't link to a javascript: address: %+v", run)
	}
	if w := get(t, handler, "/api/v1/runs/12345", true); w.Code != http.StatusNotFound {
		t.Errorf("unknown run: %d", w.Code)
	}
}

func mustFind(t *testing.T, l *Local) []LocalReport {
	t.Helper()
	reports, _ := FindReports(l.roots)
	return reports
}

func itoa(id int64) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

func TestLocal_webPage(t *testing.T) {
	l, _ := testLocal(t)
	if body := get(t, l.Handler(), "/", true).Body.String(); !strings.Contains(body, "make webui") {
		t.Errorf("a build without the page should explain how to get it: %s", body)
	}

	l.assets = fstest.MapFS{
		"index.html":     {Data: []byte("<html>app</html>")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"assets/app.css": {Data: []byte("body{}")},
	}
	handler := l.Handler()
	for target, want := range map[string]string{
		"/":              "<html>app</html>",
		"/workspaces/12": "<html>app</html>",
		"/assets/app.js": "console.log(1)",
		"/assets/":       "<html>app</html>",
	} {
		if got := get(t, handler, target, true).Body.String(); got != want {
			t.Errorf("%s: got %q, want %q", target, got, want)
		}
	}
}

func TestRepositoryFromRemote(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/acme/infra.git":         "acme/infra",
		"https://user:token@github.com/acme/infra":  "acme/infra",
		"git@github.com:acme/infra.git":             "acme/infra",
		"ssh://git@gitlab.com/group/sub/infra.git/": "group/sub/infra",
		"not a remote": "",
	} {
		if got := repositoryFromRemote(remote); got != want {
			t.Errorf("repositoryFromRemote(%q) = %q, want %q", remote, got, want)
		}
	}
}
