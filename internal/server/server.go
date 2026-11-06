// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

const maxReportBytes = 4 << 20

// Server handles the dashboard's HTTP requests.
type Server struct {
	cfg    Config
	db     *sql.DB
	auth   Authenticator
	client *http.Client
	now    func() time.Time
	logger *log.Logger
}

// New creates a Server.
func New(cfg Config, db *sql.DB) *Server {
	client := &http.Client{Timeout: 15 * time.Second}
	s := &Server{cfg: cfg, db: db, client: client, now: time.Now, logger: log.New(os.Stderr, "", log.LstdFlags)}
	if cfg.Auth.Provider == "github" {
		s.auth = &githubAuthenticator{cfg: cfg.Auth, client: client}
	} else {
		s.auth = &oidcAuthenticator{cfg: cfg.Auth, client: client}
	}
	return s
}

func (s *Server) log(format string, args ...any) { s.logger.Printf(format, args...) }

// Handler returns the HTTP handler for all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/v1/auth/callback", s.handleCallback)
	mux.HandleFunc("GET /api/v1/me", s.handleMe)

	// Pipelines authenticate with a token instead of a session.
	mux.HandleFunc("POST /api/v1/reports", s.handleReport)

	// Everything else needs a signed-in person.
	mux.HandleFunc("POST /api/v1/auth/logout", s.sameOrigin(s.handleLogout))
	mux.HandleFunc("GET /api/v1/overview", s.require(RoleViewer, s.handleOverview))
	mux.HandleFunc("GET /api/v1/workspaces", s.require(RoleViewer, s.handleWorkspaces))
	mux.HandleFunc("GET /api/v1/workspaces/{id}", s.require(RoleViewer, s.handleWorkspace))
	mux.HandleFunc("GET /api/v1/workspaces/{id}/runs", s.require(RoleViewer, s.handleWorkspaceRuns))
	mux.HandleFunc("GET /api/v1/runs/{id}", s.require(RoleViewer, s.handleRun))
	mux.HandleFunc("POST /api/v1/workspaces/{id}/runs", s.sameOrigin(s.require(RoleOperator, s.handleStartRun)))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	if s.cfg.StaticDir != "" {
		mux.Handle("/", s.staticHandler())
	}
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if s.secureCookies() {
			header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			header.Set("Cache-Control", "no-store")
		}
		if s.cfg.DevUser != "" {
			// Everyone is signed in on this mode, so a web page that rebinds its own domain to this
			// machine must not be able to read the dashboard.
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil {
				host = r.Host
			}
			if !isLoopback(strings.Trim(host, "[]")) {
				writeError(w, http.StatusForbidden, "this development server only answers on a local address")
				return
			}
		}
		if s.cfg.OriginSecret != "" {
			got := r.Header.Get("X-Bogform-Origin-Secret")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.OriginSecret)) != 1 {
				writeError(w, http.StatusForbidden, "requests must come through the configured address")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin blocks cross-site requests that change data, as defense in depth on top of SameSite cookies.
func (s *Server) sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != s.cfg.PublicURL {
			writeError(w, http.StatusForbidden, "this request must come from the dashboard")
			return
		}
		next(w, r)
	}
}

func (s *Server) require(role string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, current := s.currentUser(r)
		switch {
		case current == RoleNone:
			writeError(w, http.StatusUnauthorized, "sign in to continue")
		case role == RoleOperator && current != RoleOperator:
			writeError(w, http.StatusForbidden, "you don't have permission to do this")
		default:
			next(w, r)
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// fail logs an unexpected error. Details stay in the log, because they could reveal internals.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log("%s %s: %s", r.Method, r.URL.Path, err)
	writeError(w, http.StatusInternalServerError, "something went wrong")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		s.log("health check: %s", err)
		writeError(w, http.StatusServiceUnavailable, "the database isn't available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, role := s.currentUser(r)
	if role == RoleNone {
		writeError(w, http.StatusUnauthorized, "sign in to continue")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": identity.Email, "name": identity.Name, "role": role})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found {
		writeError(w, http.StatusUnauthorized, "a token is required")
		return
	}
	scope, valid, err := tokenScope(r.Context(), s.db, token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !valid {
		writeError(w, http.StatusUnauthorized, "the token isn't valid")
		return
	}

	var report ci.Report
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReportBytes))
	if err := decoder.Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "the report couldn't be read")
		return
	}
	if err := validateReport(&report, s.now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !scopeAllows(scope, report.Run.Platform, report.Run.Repository) {
		writeError(w, http.StatusForbidden, "this token can't report for that repository")
		return
	}
	id, err := RecordReport(r.Context(), s.db, &report)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Serverless hosting has no background jobs, so an occasional request tidies up.
	if rand.IntN(100) == 0 {
		if _, err := Prune(r.Context(), s.db, s.cfg.RetentionDays); err != nil {
			s.log("pruning old runs: %s", err)
		}
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	var overview struct {
		Workspaces int   `json:"workspaces"`
		Failing    int   `json:"failing"`
		Drifted    int   `json:"drifted"`
		RunsToday  int   `json:"runs_today"`
		RecentRuns []Run `json:"recent_runs"`
	}
	err := s.db.QueryRowContext(r.Context(), `SELECT
		COUNT(*),
		COUNT(*) FILTER (WHERE last_status = 'failed'),
		COUNT(*) FILTER (WHERE drift_status = 'drifted'),
		(SELECT COUNT(*) FROM runs WHERE finished_at > now() - interval '24 hours')
		FROM workspaces`).Scan(&overview.Workspaces, &overview.Failing, &overview.Drifted, &overview.RunsToday)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	overview.RecentRuns, err = s.listRuns(r.Context(), `TRUE`, nil, 20)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) listRuns(ctx context.Context, where string, args []any, limit int) ([]Run, error) {
	query := fmt.Sprintf(`SELECT %s FROM runs r JOIN workspaces w ON w.id = r.workspace_id WHERE %s
		ORDER BY r.finished_at DESC, r.id DESC LIMIT %d`, runColumns, where, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []Run{}
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT `+workspaceColumns+` FROM workspaces ORDER BY repository, directory, name LIMIT 1000`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	workspaces := []Workspace{}
	for rows.Next() {
		workspace, err := scanWorkspace(rows)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": workspaces})
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) loadWorkspace(w http.ResponseWriter, r *http.Request) (Workspace, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return Workspace{}, false
	}
	workspace, err := scanWorkspace(s.db.QueryRowContext(r.Context(), `SELECT `+workspaceColumns+` FROM workspaces WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return Workspace{}, false
	}
	if err != nil {
		s.fail(w, r, err)
		return Workspace{}, false
	}
	return workspace, true
}

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	workspace, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	workspace.DefaultRef = s.cfg.Triggers.Ref
	_, role := s.currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace": workspace,
		// So the dashboard can explain why the button isn't there, rather than failing when it's used.
		"can_start_run":     role == RoleOperator && s.cfg.Triggers.supports(workspace),
		"trigger_supported": s.cfg.Triggers.supports(workspace),
	})
}

// runCursor marks a position in a list of runs, so that new runs arriving don't shift later pages.
type runCursor struct {
	FinishedAt time.Time `json:"t"`
	ID         int64     `json:"i"`
}

func (s *Server) handleWorkspaceRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	const pageSize = 50
	where := `r.workspace_id = $1`
	args := []any{id}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var cursor runCursor
		if err != nil || json.Unmarshal(decoded, &cursor) != nil {
			writeError(w, http.StatusBadRequest, "the cursor isn't valid")
			return
		}
		where += ` AND (r.finished_at, r.id) < ($2, $3)`
		args = append(args, cursor.FinishedAt, cursor.ID)
	}
	runs, err := s.listRuns(r.Context(), where, args, pageSize)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	response := map[string]any{"runs": runs}
	if len(runs) == pageSize {
		last := runs[len(runs)-1]
		raw, _ := json.Marshal(runCursor{FinishedAt: last.FinishedAt, ID: last.ID})
		response["next_cursor"] = base64.RawURLEncoding.EncodeToString(raw)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var changes []byte
	run, err := scanRun(s.db.QueryRowContext(r.Context(), `SELECT `+runColumns+`, r.changes FROM runs r JOIN workspaces w ON w.id = r.workspace_id WHERE r.id = $1`, id), &changes)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := json.Unmarshal(changes, &run.Changes); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	workspace, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	if !s.cfg.Triggers.supports(workspace) {
		writeError(w, http.StatusBadRequest, "this server isn't set up to start pipelines on "+workspace.Platform)
		return
	}
	workspace.DefaultRef = s.cfg.Triggers.Ref
	identity, _ := s.currentUser(r)
	// Record the attempt first, so that the log is complete even if the platform's response is lost.
	detail := map[string]string{"platform": workspace.Platform, "ref": workspace.DefaultRef}
	target := fmt.Sprintf("%s:%s:%s", workspace.Repository, workspace.Directory, workspace.Name)
	if err := recordAudit(r.Context(), s.db, identity.Email, "run.start", target, detail); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.cfg.Triggers.start(r.Context(), s.client, workspace); err != nil {
		s.log("starting a pipeline for %s: %s", target, err)
		writeError(w, http.StatusBadGateway, "the pipeline platform refused the request. Check the server's log for details")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

// staticHandler serves the built UI. Any path that isn't a file gets index.html, for client-side routes.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServer(http.Dir(s.cfg.StaticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean("/" + r.URL.Path)
		info, err := os.Stat(filepath.Join(s.cfg.StaticDir, filepath.FromSlash(clean)))
		if err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(s.cfg.StaticDir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
