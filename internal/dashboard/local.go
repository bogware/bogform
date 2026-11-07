// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package dashboard

import (
	"cmp"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

// ReportFile is where "bogform ci" writes its report, relative to the configuration directory.
const ReportFile = ".bogform-ci/report.json"

// maxReportSize bounds what's read from one report file.
const maxReportSize = 4 << 20

// skipDirs aren't searched for reports, because they're large and never hold any.
var skipDirs = map[string]bool{".git": true, "node_modules": true, ".terraform": true, "vendor": true}

// Local serves the dashboard's read-only API from report files on this computer, for "bogform ui".
type Local struct {
	roots  []string
	token  string
	assets fs.FS
	now    func() time.Time
	// rescan is how long a scan is reused, so that loading one page doesn't search the disk repeatedly.
	rescan time.Duration
	// repository works out a repository's name for reports made outside a pipeline.
	repository func(dir string) string

	mu      sync.Mutex
	scanned time.Time
	data    *localData
}

// NewLocal creates a local dashboard over the reports under roots. Every request must carry token, which
// the person gets in the address that "bogform ui" prints. assets is the built web page, or nil.
func NewLocal(roots []string, token string, assets fs.FS) *Local {
	return &Local{roots: roots, token: token, assets: assets, now: time.Now, rescan: 2 * time.Second, repository: gitRepositoryName}
}

type localData struct {
	workspaces map[int64]*localWorkspace
	runs       map[int64]Run
}

type localWorkspace struct {
	Workspace
	runs []Run
}

// LocalReport is a report file that was found, and the run it describes.
type LocalReport struct {
	Path   string
	Report ci.Report
}

// FindReports searches roots for report files. Files that can't be read are skipped, and returned as
// problems, so that one broken file doesn't hide the rest.
func FindReports(roots []string) ([]LocalReport, []error) {
	var reports []LocalReport
	var problems []error
	seen := map[string]bool{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable directory is skipped rather than stopping the whole search.
				if entry != nil && entry.IsDir() && p != root {
					return fs.SkipDir
				}
				return err
			}
			if entry.IsDir() {
				if skipDirs[entry.Name()] && p != root {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Name() != "report.json" || filepath.Base(filepath.Dir(p)) != ".bogform-ci" {
				return nil
			}
			absolute, _ := filepath.Abs(p)
			if seen[absolute] {
				return nil
			}
			seen[absolute] = true
			report, err := readReport(p)
			if err != nil {
				problems = append(problems, err)
				return nil
			}
			reports = append(reports, LocalReport{Path: absolute, Report: report})
			return nil
		})
		if err != nil {
			problems = append(problems, err)
		}
	}
	return reports, problems
}

func readReport(p string) (ci.Report, error) {
	var report ci.Report
	file, err := os.Open(p)
	if err != nil {
		return report, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxReportSize+1))
	if err != nil {
		return report, err
	}
	if len(raw) > maxReportSize {
		return report, errors.New(p + " is too large to be a report")
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return report, errors.New(p + " isn't a valid report")
	}
	if report.FormatVersion != ci.ReportFormatVersion {
		return report, errors.New(p + " has an unsupported format version")
	}
	return report, nil
}

// stableID makes a positive ID that a JavaScript number holds exactly, and that stays the same between
// scans, so that links keep working when reports change.
func stableID(parts ...string) int64 {
	h := fnv.New64a()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	id := int64(h.Sum64() & (1<<53 - 1))
	if id == 0 {
		id = 1
	}
	return id
}

// load builds the dashboard's records from reports.
func (l *Local) load(reports []LocalReport) *localData {
	data := &localData{workspaces: map[int64]*localWorkspace{}, runs: map[int64]Run{}}
	for _, found := range reports {
		report := found.Report
		configDir := filepath.Dir(filepath.Dir(found.Path))
		platform := cmp.Or(report.Run.Platform, "local")
		repository := cmp.Or(report.Run.Repository, l.repository(configDir))
		directory := cmp.Or(report.Directory, ".")
		workspaceName := cmp.Or(report.Workspace, "default")

		id := stableID(platform, repository, directory, workspaceName)
		workspace := data.workspaces[id]
		if workspace == nil {
			workspace = &localWorkspace{Workspace: Workspace{ID: id, Platform: platform, Repository: repository, Directory: directory, Name: workspaceName}}
			data.workspaces[id] = workspace
		}
		run := Run{
			ID:             stableID(found.Path, report.FinishedAt.UTC().Format(time.RFC3339Nano)),
			WorkspaceID:    id,
			Repository:     repository,
			Directory:      directory,
			WorkspaceName:  workspaceName,
			Kind:           report.Kind,
			Status:         report.Status,
			Branch:         report.Run.Branch,
			Commit:         report.Run.Commit,
			PullRequest:    report.Run.PullRequest,
			BogformVersion: report.BogformVersion,
			Counts:         report.Counts,
			Changes:        report.Changes,
			OutputsChanged: report.OutputsChanged,
			Error:          report.Error,
			StartedAt:      report.StartedAt,
			FinishedAt:     report.FinishedAt,
		}
		// Only link to pages on the web, never to anything a file could point a browser at.
		if strings.HasPrefix(report.Run.RunURL, "https://") || strings.HasPrefix(report.Run.RunURL, "http://") {
			run.RunURL = report.Run.RunURL
		}
		if !report.Run.IsPullRequest {
			run.PullRequest = ""
		}
		workspace.runs = append(workspace.runs, run)
		data.runs[run.ID] = run
	}
	for _, workspace := range data.workspaces {
		slices.SortFunc(workspace.runs, newestFirst)
		w := &workspace.Workspace
		latest := workspace.runs[0]
		w.LastStatus, w.LastKind, w.LastRunAt = latest.Status, latest.Kind, latest.FinishedAt
		for _, run := range workspace.runs {
			if run.Kind == ci.KindApply && run.Status != ci.StatusFailed && w.LastApplyAt == nil {
				at := run.FinishedAt
				w.LastApplyAt = &at
			}
			if run.Kind == ci.KindDrift && run.Status != ci.StatusFailed && w.DriftCheckAt == nil {
				at := run.FinishedAt
				w.DriftStatus, w.DriftCheckAt = run.Status, &at
			}
		}
		// An apply after the latest drift check should have fixed it, as on the server.
		if w.DriftCheckAt != nil && w.LastApplyAt != nil && w.LastApplyAt.After(*w.DriftCheckAt) {
			w.DriftStatus = ""
		}
	}
	return data
}

func newestFirst(a, b Run) int {
	return cmp.Or(b.FinishedAt.Compare(a.FinishedAt), cmp.Compare(b.ID, a.ID))
}

func (l *Local) current() *localData {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.data == nil || l.now().Sub(l.scanned) >= l.rescan {
		reports, _ := FindReports(l.roots)
		l.data = l.load(reports)
		l.scanned = l.now()
	}
	return l.data
}

// gitRepositoryName names the repository that dir is in, from its origin remote, such as "acme/infra".
// Without one, it uses the repository's folder name.
func gitRepositoryName(dir string) string {
	top, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		absolute, _ := filepath.Abs(dir)
		return filepath.Base(absolute)
	}
	root := strings.TrimSpace(string(top))
	if remote, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		if name := repositoryFromRemote(strings.TrimSpace(string(remote))); name != "" {
			return name
		}
	}
	return filepath.Base(root)
}

// repositoryFromRemote turns a remote URL into a repository's path, such as "acme/infra". Credentials in
// the URL are dropped.
func repositoryFromRemote(remote string) string {
	remote = strings.TrimSuffix(strings.TrimSuffix(remote, "/"), ".git")
	if parsed, err := url.Parse(remote); err == nil && parsed.Host != "" {
		return strings.Trim(parsed.Path, "/")
	}
	// The scp-like form, user@host:owner/name.
	if _, rest, found := strings.Cut(remote, ":"); found && !strings.Contains(remote, "://") {
		return strings.Trim(rest, "/")
	}
	return ""
}

// cookieName includes the port, because cookies are shared between ports, and two "bogform ui" processes
// on different ports would otherwise replace each other's.
func cookieName(r *http.Request) string {
	_, port, _ := net.SplitHostPort(r.Host)
	return "bogform_ui_" + port
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handler serves the API and the web page.
func (l *Local) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"name": "Local", "email": "", "role": "viewer", "local": true})
	})
	mux.HandleFunc("GET /api/v1/overview", l.handleOverview)
	mux.HandleFunc("GET /api/v1/workspaces", l.handleWorkspaces)
	mux.HandleFunc("GET /api/v1/workspaces/{id}", l.handleWorkspace)
	mux.HandleFunc("GET /api/v1/workspaces/{id}/runs", l.handleWorkspaceRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}", l.handleRun)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found. This local view is read-only")
	})
	mux.Handle("/", l.staticHandler())
	return l.middleware(mux)
}

func (l *Local) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		header.Set("Cache-Control", "no-store")

		// A web page that rebinds its own domain to this machine must not be able to read the reports.
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if !isLoopback(strings.Trim(host, "[]")) {
			writeError(w, http.StatusForbidden, "this local view only answers on a local address")
			return
		}

		// The address that "bogform ui" prints carries the token. Swap it for a cookie, and take it out
		// of the address so that it doesn't stay in the browser's history.
		if token := r.URL.Query().Get("token"); token != "" {
			if subtle.ConstantTimeCompare([]byte(token), []byte(l.token)) != 1 {
				writeError(w, http.StatusUnauthorized, "that link isn't valid. Use the address that bogform ui printed")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: cookieName(r), Value: l.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			clean := *r.URL
			query := clean.Query()
			query.Del("token")
			clean.RawQuery = query.Encode()
			http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
			return
		}
		cookie, err := r.Cookie(cookieName(r))
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(l.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "open the address that bogform ui printed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (l *Local) sortedWorkspaces(data *localData) []*localWorkspace {
	workspaces := make([]*localWorkspace, 0, len(data.workspaces))
	for _, workspace := range data.workspaces {
		workspaces = append(workspaces, workspace)
	}
	slices.SortFunc(workspaces, func(a, b *localWorkspace) int {
		return cmp.Or(strings.Compare(a.Repository, b.Repository), strings.Compare(a.Directory, b.Directory), strings.Compare(a.Name, b.Name))
	})
	return workspaces
}

func summarize(runs []Run, kind string) *RunSummary {
	for _, run := range runs {
		if run.Kind == kind {
			return &RunSummary{ID: run.ID, Status: run.Status, FinishedAt: run.FinishedAt, PullRequest: run.PullRequest, Counts: run.Counts}
		}
	}
	return nil
}

func (l *Local) handleOverview(w http.ResponseWriter, _ *http.Request) {
	data := l.current()
	overview := struct {
		Workspaces   int                  `json:"workspaces"`
		Failing      int                  `json:"failing"`
		Drifted      int                  `json:"drifted"`
		RunsToday    int                  `json:"runs_today"`
		RecentRuns   []Run                `json:"recent_runs"`
		Repositories []RepositoryOverview `json:"repositories"`
	}{RecentRuns: []Run{}}
	var items []WorkspaceOverview
	for _, workspace := range l.sortedWorkspaces(data) {
		overview.Workspaces++
		if workspace.LastStatus == ci.StatusFailed {
			overview.Failing++
		}
		if workspace.DriftStatus == ci.StatusDrifted {
			overview.Drifted++
		}
		items = append(items, WorkspaceOverview{
			Workspace: workspace.Workspace,
			LastPlan:  summarize(workspace.runs, ci.KindPlan),
			LastApply: summarize(workspace.runs, ci.KindApply),
			LastDrift: summarize(workspace.runs, ci.KindDrift),
		})
	}
	dayAgo := l.now().Add(-24 * time.Hour)
	for _, run := range data.runs {
		if run.FinishedAt.After(dayAgo) {
			overview.RunsToday++
		}
		overview.RecentRuns = append(overview.RecentRuns, withoutChanges(run))
	}
	slices.SortFunc(overview.RecentRuns, newestFirst)
	if len(overview.RecentRuns) > 20 {
		overview.RecentRuns = overview.RecentRuns[:20]
	}
	overview.Repositories = GroupRepositories(items)
	writeJSON(w, http.StatusOK, overview)
}

// withoutChanges leaves out the list of changes, which lists don't show.
func withoutChanges(run Run) Run {
	run.Changes = nil
	return run
}

func (l *Local) handleWorkspaces(w http.ResponseWriter, _ *http.Request) {
	workspaces := []Workspace{}
	for _, workspace := range l.sortedWorkspaces(l.current()) {
		workspaces = append(workspaces, workspace.Workspace)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces})
}

func (l *Local) findWorkspace(w http.ResponseWriter, r *http.Request) (*localWorkspace, bool) {
	id, ok := pathID(r)
	workspace := l.current().workspaces[id]
	if !ok || workspace == nil {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return workspace, true
}

func (l *Local) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	workspace, ok := l.findWorkspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace":         workspace.Workspace,
		"can_start_run":     false,
		"trigger_supported": false,
		"local":             true,
	})
}

func (l *Local) handleWorkspaceRuns(w http.ResponseWriter, r *http.Request) {
	workspace, ok := l.findWorkspace(w, r)
	if !ok {
		return
	}
	runs := make([]Run, 0, len(workspace.runs))
	for _, run := range workspace.runs {
		runs = append(runs, withoutChanges(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (l *Local) handleRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	run, found := l.current().runs[id]
	if !ok || !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if run.Changes == nil {
		run.Changes = []ci.Change{}
	}
	writeJSON(w, http.StatusOK, run)
}

// missingPage is shown by builds that don't include the web page, such as development builds.
const missingPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Bogform</title></head>
<body>
<h1>Bogform</h1>
<p>This build of bogform doesn't include the dashboard's web page. Release builds do.</p>
<p>To include it in your own build, run <code>make webui</code> and then build again.</p>
<p>The data is still available at <a href="/api/v1/overview">/api/v1/overview</a>.</p>
</body>
</html>
`

// staticHandler serves the web page. Any path that isn't a file gets index.html, for the page's own routes.
func (l *Local) staticHandler() http.Handler {
	if l.assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, missingPage)
		})
	}
	files := http.FileServerFS(l.assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if info, err := fs.Stat(l.assets, name); name == "" || err != nil || info.IsDir() {
			http.ServeFileFS(w, r, l.assets, "index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}
