// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/bogware/bogform/internal/ci"
)

// These tests need a real Postgres, because the queries are much of what they check. Set
// BOG_TEST_DATABASE_URL to run them, for example from the Docker Compose file in deploy/local.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BOG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BOG_TEST_DATABASE_URL isn't set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `TRUNCATE ingest_tokens, workspaces, runs, audit_log RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	return db
}

var testKey = bytes.Repeat([]byte{7}, 32)

func testConfig() Config {
	return Config{
		PublicURL:   "https://bogform.example.com",
		DatabaseURL: "unused",
		SessionKey:  testKey,
		Viewers:     []string{"viewer@example.com", "@viewers.example.com"},
		Operators:   []string{"operator@example.com"},
		Auth:        AuthConfig{Provider: "oidc", Issuer: "https://idp.example.com", ClientID: "client", ClientSecret: "secret"},
		Triggers:    TriggerConfig{Ref: "main", AzurePipelines: map[string]string{}, GitHubWorkflow: "bogform.yml"},
	}
}

func testServer(t *testing.T, cfg Config, db *sql.DB) *Server {
	t.Helper()
	s := New(cfg, db)
	s.logger = log.New(io.Discard, "", 0)
	return s
}

// signedIn returns a request carrying a session for email.
func signedIn(t *testing.T, s *Server, method, target, email string, body io.Reader) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, body)
	if email != "" {
		value, err := s.sign(sessionCookie, sessionData{Email: email, Expires: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookie), Value: value})
	}
	r.Header.Set("Origin", s.cfg.PublicURL)
	return r
}

func testReport(repository string) *ci.Report {
	now := time.Now().UTC().Truncate(time.Second)
	return &ci.Report{
		FormatVersion: ci.ReportFormatVersion, Kind: ci.KindPlan, Status: ci.StatusChanges, BogformVersion: "1.0.0",
		Directory: "infra", Workspace: "default",
		Run:       ci.Context{Platform: ci.PlatformGitHub, Repository: repository, Branch: "feature", Commit: "abc123", RunURL: "https://github.com/run/1", IsPullRequest: true, PullRequest: "4"},
		Counts:    ci.Counts{Add: 1},
		Changes:   []ci.Change{{Address: "aws_s3_bucket.a", Actions: []string{"create"}}},
		StartedAt: now.Add(-time.Minute), FinishedAt: now,
	}
}

func postReport(t *testing.T, handler http.Handler, token string, report any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/reports", bytes.NewReader(raw))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestRoleFor(t *testing.T) {
	cfg := testConfig()
	for email, want := range map[string]string{
		"viewer@example.com":         RoleViewer,
		"VIEWER@example.com":         RoleViewer,
		"anyone@viewers.example.com": RoleViewer,
		"operator@example.com":       RoleOperator,
		"stranger@example.com":       RoleNone,
		"a@notviewers.example.com":   RoleNone,
		"viewers.example.com":        RoleNone,
		"":                           RoleNone,
	} {
		if got := cfg.RoleFor(email); got != want {
			t.Errorf("RoleFor(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	valid := map[string]string{
		"BOG_SERVER_PUBLIC_URL":         "https://bogform.example.com",
		"BOG_SERVER_DATABASE_URL":       "postgres://x",
		"BOG_SERVER_SESSION_KEY":        strings.Repeat("ab", 32),
		"BOG_SERVER_VIEWERS":            "a@example.com, @Example.org",
		"BOG_SERVER_OIDC_ISSUER":        "https://idp.example.com/",
		"BOG_SERVER_OIDC_CLIENT_ID":     "id",
		"BOG_SERVER_OIDC_CLIENT_SECRET": "secret",
		"BOG_SERVER_AZURE_PIPELINES":    "repo=project/12",
	}
	load := func(changes map[string]string) (Config, error) {
		return LoadConfig(func(name string) string {
			if value, ok := changes[name]; ok {
				return value
			}
			return valid[name]
		})
	}
	cfg, err := load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Issuer != "https://idp.example.com" || cfg.Viewers[1] != "@example.org" || cfg.Triggers.AzurePipelines["repo"] != "project/12" || !cfg.AutoMigrate || cfg.RetentionDays != 365 {
		t.Fatalf("wrong configuration: %+v", cfg)
	}

	for name, changes := range map[string]map[string]string{
		"plain http":             {"BOG_SERVER_PUBLIC_URL": "http://bogform.example.com"},
		"path in URL":            {"BOG_SERVER_PUBLIC_URL": "https://example.com/bogform"},
		"no database":            {"BOG_SERVER_DATABASE_URL": ""},
		"placeholder database":   {"BOG_SERVER_DATABASE_URL": PlaceholderSecret},
		"short key":              {"BOG_SERVER_SESSION_KEY": "abcd"},
		"placeholder key":        {"BOG_SERVER_SESSION_KEY": PlaceholderSecret},
		"key isn't hexadecimal":  {"BOG_SERVER_SESSION_KEY": strings.Repeat("z", 64)},
		"nobody can sign in":     {"BOG_SERVER_VIEWERS": ""},
		"no issuer":              {"BOG_SERVER_OIDC_ISSUER": ""},
		"placeholder secret":     {"BOG_SERVER_OIDC_CLIENT_SECRET": PlaceholderSecret},
		"unknown provider":       {"BOG_SERVER_AUTH_PROVIDER": "magic"},
		"dev user in production": {"BOG_SERVER_DEV_USER": "me@example.com"},
		"placeholder origin":     {"BOG_SERVER_ORIGIN_SECRET": PlaceholderSecret},
		"bad retention":          {"BOG_SERVER_RETENTION_DAYS": "soon"},
		"bad pipeline mapping":   {"BOG_SERVER_AZURE_PIPELINES": "repo"},
		"bad trigger ref":        {"BOG_SERVER_TRIGGER_REF": "--upload-pack=evil"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(changes); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	t.Run("dev user on loopback", func(t *testing.T) {
		_, err := load(map[string]string{"BOG_SERVER_PUBLIC_URL": "http://localhost:8080", "BOG_SERVER_DEV_USER": "me@example.com", "BOG_SERVER_VIEWERS": "", "BOG_SERVER_OIDC_ISSUER": ""})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessions(t *testing.T) {
	s := testServer(t, testConfig(), nil)
	user := func(r *http.Request) string {
		identity, role := s.currentUser(r)
		return identity.Email + "/" + role
	}
	if got := user(signedIn(t, s, "GET", "/", "viewer@example.com", nil)); got != "viewer@example.com/viewer" {
		t.Fatalf("valid session: %q", got)
	}
	if got := user(httptest.NewRequest("GET", "/", nil)); got != "/" {
		t.Fatalf("no session: %q", got)
	}

	t.Run("expired", func(t *testing.T) {
		value, _ := s.sign(sessionCookie, sessionData{Email: "viewer@example.com", Expires: time.Now().Add(-time.Second).Unix()})
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookie), Value: value})
		if got := user(r); got != "/" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("tampered", func(t *testing.T) {
		value, _ := s.sign(sessionCookie, sessionData{Email: "viewer@example.com", Expires: time.Now().Add(time.Hour).Unix()})
		_, signature, _ := strings.Cut(value, ".")
		forged, _ := json.Marshal(sessionData{Email: "operator@example.com", Expires: time.Now().Add(time.Hour).Unix()})
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookie), Value: base64.RawURLEncoding.EncodeToString(forged) + "." + signature})
		if got := user(r); got != "/" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("signed with another key", func(t *testing.T) {
		cfg := testConfig()
		cfg.SessionKey = bytes.Repeat([]byte{9}, 32)
		other := testServer(t, cfg, nil)
		if got := user(signedIn(t, other, "GET", "/", "viewer@example.com", nil)); got != "/" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("another purpose", func(t *testing.T) {
		value, _ := s.sign(loginCookie, sessionData{Email: "viewer@example.com", Expires: time.Now().Add(time.Hour).Unix()})
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: s.cookieName(sessionCookie), Value: value})
		if got := user(r); got != "/" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("removed from the allowlist", func(t *testing.T) {
		if got := user(signedIn(t, s, "GET", "/", "stranger@example.com", nil)); got != "stranger@example.com/" {
			t.Fatalf("got %q", got)
		}
	})
}

// TestAuthorization checks every route, so that adding one without protection fails here.
func TestAuthorization(t *testing.T) {
	db := testDB(t)
	cfg := testConfig()
	cfg.Triggers.GitHubToken = "t"
	cfg.Triggers.GitHubAPIURL = "http://127.0.0.1:1"
	s := testServer(t, cfg, db)
	handler := s.Handler()
	if _, err := RecordReport(t.Context(), db, testReport("acme/infra")); err != nil {
		t.Fatal(err)
	}

	routes := []struct {
		method, path string
		minimum      string
	}{
		{"GET", "/api/v1/overview", RoleViewer},
		{"GET", "/api/v1/workspaces", RoleViewer},
		{"GET", "/api/v1/workspaces/1", RoleViewer},
		{"GET", "/api/v1/workspaces/1/runs", RoleViewer},
		{"GET", "/api/v1/runs/1", RoleViewer},
		{"POST", "/api/v1/workspaces/1/runs", RoleOperator},
	}
	users := map[string]string{"": RoleNone, "stranger@example.com": RoleNone, "viewer@example.com": RoleViewer, "operator@example.com": RoleOperator}
	for _, route := range routes {
		for email, role := range users {
			t.Run(fmt.Sprintf("%s %s as %q", route.method, route.path, email), func(t *testing.T) {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, signedIn(t, s, route.method, route.path, email, nil))
				switch {
				case role == RoleNone && w.Code != http.StatusUnauthorized:
					t.Fatalf("expected 401, got %d", w.Code)
				case role == RoleViewer && route.minimum == RoleOperator && w.Code != http.StatusForbidden:
					t.Fatalf("expected 403, got %d", w.Code)
				case role != RoleNone && !(role == RoleViewer && route.minimum == RoleOperator) && (w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden):
					t.Fatalf("expected access, got %d", w.Code)
				}
			})
		}
	}
}

func TestStateChangesRequireSameOrigin(t *testing.T) {
	db := testDB(t)
	s := testServer(t, testConfig(), db)
	for _, origin := range []string{"", "https://evil.example.com", "https://bogform.example.com.evil.example.com"} {
		r := signedIn(t, s, "POST", "/api/v1/workspaces/1/runs", "operator@example.com", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("origin %q: got %d", origin, w.Code)
		}
	}
}

func TestOriginSecret(t *testing.T) {
	db := testDB(t)
	cfg := testConfig()
	cfg.OriginSecret = "from-cloudfront"
	handler := testServer(t, cfg, db).Handler()

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/health", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("without the secret: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/v1/health", nil)
	r.Header.Set("X-Bogform-Origin-Secret", "from-cloudfront")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("with the secret: %d", w.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := testServer(t, testConfig(), nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nope", nil))
	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Cache-Control": "no-store",
		"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") {
		t.Errorf("weak CSP: %q", csp)
	}
}

func TestReports(t *testing.T) {
	db := testDB(t)
	s := testServer(t, testConfig(), db)
	handler := s.Handler()
	ctx := t.Context()

	token, err := CreateToken(ctx, db, "acme", "acme/*")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		t.Fatalf("wrong token %q", token)
	}
	var stored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ingest_tokens WHERE token_hash = $1`, []byte(token)).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("the token must not be stored in plain text: %d, %v", stored, err)
	}

	if w := postReport(t, handler, "", testReport("acme/infra")); w.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", w.Code)
	}
	if w := postReport(t, handler, "bogform_wrong", testReport("acme/infra")); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := postReport(t, handler, token, testReport("other/infra")); w.Code != http.StatusForbidden {
		t.Errorf("repository outside the scope: %d", w.Code)
	}
	if w := postReport(t, handler, token, testReport("acme/deep/infra")); w.Code != http.StatusForbidden {
		t.Errorf("a glob shouldn't cross a slash: %d", w.Code)
	}

	for name, change := range map[string]func(*ci.Report){
		"format":              func(r *ci.Report) { r.FormatVersion = 99 },
		"kind":                func(r *ci.Report) { r.Kind = "destroy" },
		"status":              func(r *ci.Report) { r.Status = "great" },
		"platform":            func(r *ci.Report) { r.Run.Platform = ci.PlatformNone },
		"javascript":          func(r *ci.Report) { r.Run.RunURL = "javascript:alert(1)" },
		"data URL":            func(r *ci.Report) { r.Run.RunURL = "data:text/html,hi" },
		"commit":              func(r *ci.Report) { r.Run.Commit = "<script>" },
		"long field":          func(r *ci.Report) { r.Directory = strings.Repeat("x", maxFieldLength+1) },
		"no directory":        func(r *ci.Report) { r.Directory = "" },
		"no time":             func(r *ci.Report) { r.FinishedAt = time.Time{} },
		"future time":         func(r *ci.Report) { r.FinishedAt = time.Now().Add(24 * time.Hour) },
		"query in repository": func(r *ci.Report) { r.Run.Repository = "acme/x?evil=1" },
		"traversal":           func(r *ci.Report) { r.Run.Repository = "acme/../../admin" },
		"dot segment":         func(r *ci.Report) { r.Run.Repository = "acme/." },
		"encoded path":        func(r *ci.Report) { r.Run.Repository = "acme/%2e%2e" },
	} {
		t.Run("rejects bad "+name, func(t *testing.T) {
			report := testReport("acme/infra")
			change(report)
			if w := postReport(t, handler, token, report); w.Code != http.StatusBadRequest {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
		})
	}

	if w := postReport(t, handler, token, testReport("acme/infra")); w.Code != http.StatusCreated {
		t.Fatalf("valid report: %d %s", w.Code, w.Body)
	}

	get := func(path string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, signedIn(t, s, "GET", path, "viewer@example.com", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var overview struct {
		Workspaces, Failing, Drifted int
		RunsToday                    int   `json:"runs_today"`
		RecentRuns                   []Run `json:"recent_runs"`
	}
	get("/api/v1/overview", &overview)
	if overview.Workspaces != 1 || overview.RunsToday != 1 || len(overview.RecentRuns) != 1 {
		t.Fatalf("wrong overview: %+v", overview)
	}
	if overview.RecentRuns[0].Repository != "acme/infra" || overview.RecentRuns[0].Counts.Add != 1 {
		t.Fatalf("wrong run: %+v", overview.RecentRuns[0])
	}

	var run Run
	get("/api/v1/runs/1", &run)
	if len(run.Changes) != 1 || run.Changes[0].Address != "aws_s3_bucket.a" {
		t.Fatalf("wrong run: %+v", run)
	}

	// Revoked tokens stop working.
	if err := RevokeToken(ctx, db, "acme"); err != nil {
		t.Fatal(err)
	}
	if w := postReport(t, handler, token, testReport("acme/infra")); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", w.Code)
	}
	if err := RevokeToken(ctx, db, "acme"); err == nil {
		t.Error("revoking twice should say there's no such active token")
	}
	tokens, err := ListTokens(ctx, db)
	if err != nil || len(tokens) != 1 || tokens[0].RevokedAt == nil || tokens[0].LastUsedAt == nil {
		t.Fatalf("wrong tokens: %+v %v", tokens, err)
	}
}

func TestWorkspaceStatus(t *testing.T) {
	db := testDB(t)
	ctx := t.Context()
	at := time.Now().UTC().Truncate(time.Second)
	record := func(kind, status string, minutes int, pullRequest bool) {
		t.Helper()
		report := testReport("acme/infra")
		report.Kind, report.Status = kind, status
		report.FinishedAt = at.Add(time.Duration(minutes) * time.Minute)
		report.Run.IsPullRequest = pullRequest
		report.Run.Branch = "main"
		if _, err := RecordReport(ctx, db, report); err != nil {
			t.Fatal(err)
		}
	}
	load := func() Workspace {
		t.Helper()
		w, err := scanWorkspace(db.QueryRow(`SELECT ` + workspaceColumns + ` FROM workspaces`))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	record(ci.KindApply, ci.StatusApplied, 0, false)
	if w := load(); w.LastStatus != ci.StatusApplied || w.LastApplyAt == nil {
		t.Fatalf("after apply: %+v", w)
	}
	record(ci.KindDrift, ci.StatusDrifted, 1, false)
	if w := load(); w.DriftStatus != ci.StatusDrifted || w.DriftCheckAt == nil {
		t.Fatalf("after drift: %+v", w)
	}
	// A plan on a pull request says nothing about real infrastructure, so neither status changes.
	record(ci.KindPlan, ci.StatusFailed, 2, true)
	if w := load(); w.DriftStatus != ci.StatusDrifted || w.LastStatus != ci.StatusDrifted {
		t.Fatalf("after plan: %+v", w)
	}
	// A report arriving late doesn't replace newer status.
	record(ci.KindApply, ci.StatusFailed, -60, false)
	if w := load(); w.LastStatus != ci.StatusDrifted {
		t.Fatalf("after a late report: %+v", w)
	}
	// An apply clears drift.
	record(ci.KindApply, ci.StatusApplied, 3, false)
	if w := load(); w.DriftStatus != "" {
		t.Fatalf("after apply: %+v", w)
	}
	// A failed apply doesn't.
	record(ci.KindDrift, ci.StatusDrifted, 4, false)
	record(ci.KindApply, ci.StatusFailed, 5, false)
	if w := load(); w.DriftStatus != ci.StatusDrifted {
		t.Fatalf("after a failed apply: %+v", w)
	}
}

func TestWorkspaceStatus_pullRequestOnly(t *testing.T) {
	db := testDB(t)
	report := testReport("acme/infra")
	report.Status = ci.StatusChanges
	if _, err := RecordReport(t.Context(), db, report); err != nil {
		t.Fatal(err)
	}
	w, err := scanWorkspace(db.QueryRow(`SELECT ` + workspaceColumns + ` FROM workspaces`))
	if err != nil || w.LastStatus != ci.StatusChanges {
		t.Fatalf("with no other runs, a pull request's status is the best available: %+v %v", w, err)
	}
}

func TestPagination(t *testing.T) {
	db := testDB(t)
	s := testServer(t, testConfig(), db)
	at := time.Now().UTC().Truncate(time.Second)
	for i := range 120 {
		report := testReport("acme/infra")
		report.FinishedAt = at.Add(-time.Duration(i) * time.Minute)
		if _, err := RecordReport(t.Context(), db, report); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	cursor := ""
	for range 10 {
		target := "/api/v1/workspaces/1/runs"
		if cursor != "" {
			target += "?cursor=" + url.QueryEscape(cursor)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, signedIn(t, s, "GET", target, "viewer@example.com", nil))
		var page struct {
			Runs       []Run  `json:"runs"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Runs {
			if seen[run.ID] {
				t.Fatalf("run %d appeared twice", run.ID)
			}
			seen[run.ID] = true
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	if len(seen) != 120 {
		t.Fatalf("saw %d runs", len(seen))
	}
}

func TestPrune(t *testing.T) {
	db := testDB(t)
	report := testReport("acme/infra")
	report.FinishedAt = time.Now().Add(-48 * time.Hour)
	if _, err := RecordReport(t.Context(), db, report); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordReport(t.Context(), db, testReport("acme/infra")); err != nil {
		t.Fatal(err)
	}
	if count, err := Prune(t.Context(), db, 0); err != nil || count != 0 {
		t.Fatalf("zero keeps everything: %d %v", count, err)
	}
	if count, err := Prune(t.Context(), db, 1); err != nil || count != 1 {
		t.Fatalf("wrong count: %d %v", count, err)
	}
}

func TestStartRun(t *testing.T) {
	type call struct {
		path, auth string
		body       map[string]any
	}
	tests := map[string]struct {
		platform   string
		repository string
		configure  func(*TriggerConfig, string)
		wantPath   string
		wantBody   string
	}{
		"github": {ci.PlatformGitHub, "acme/infra", func(c *TriggerConfig, u string) { c.GitHubToken, c.GitHubAPIURL = "tok", u },
			"/repos/acme/infra/actions/workflows/bogform.yml/dispatches", `"ref":"main"`},
		"gitlab": {ci.PlatformGitLab, "acme/infra", func(c *TriggerConfig, u string) { c.GitLabToken, c.GitLabAPIURL = "tok", u },
			"/projects/acme%2Finfra/pipeline", `"ref":"main"`},
		"bitbucket": {ci.PlatformBitbucket, "acme/infra", func(c *TriggerConfig, u string) { c.BitbucketToken, c.BitbucketURL = "tok", u },
			"/repositories/acme/infra/pipelines/", `"ref_name":"main"`},
		"azure": {ci.PlatformAzure, "infra", func(c *TriggerConfig, u string) {
			c.AzureToken, c.AzureOrgURL = "tok", u
			c.AzurePipelines["infra"] = "proj/12"
		}, "/proj/_apis/pipelines/12/runs", `"refName":"refs/heads/main"`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			var mu sync.Mutex
			var got []call
			var rawBody string
			platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				raw, _ := io.ReadAll(r.Body)
				rawBody = string(raw)
				got = append(got, call{path: r.URL.EscapedPath(), auth: r.Header.Get("Authorization") + r.Header.Get("PRIVATE-TOKEN")})
				w.WriteHeader(http.StatusNoContent)
			}))
			defer platform.Close()

			cfg := testConfig()
			test.configure(&cfg.Triggers, platform.URL)
			s := testServer(t, cfg, db)
			report := testReport(test.repository)
			report.Run.Platform = test.platform
			if _, err := RecordReport(t.Context(), db, report); err != nil {
				t.Fatal(err)
			}

			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, signedIn(t, s, "POST", "/api/v1/workspaces/1/runs", "operator@example.com", nil))
			if w.Code != http.StatusAccepted {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
			wantAuth := "tok"
			if test.platform == ci.PlatformAzure {
				wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(":tok"))
			}
			if len(got) != 1 || got[0].path != test.wantPath || !strings.Contains(got[0].auth, wantAuth) {
				t.Fatalf("wrong call: %+v", got)
			}
			if !strings.Contains(rawBody, test.wantBody) {
				t.Fatalf("wrong body: %s", rawBody)
			}
			var actor, action string
			if err := db.QueryRow(`SELECT actor, action FROM audit_log`).Scan(&actor, &action); err != nil {
				t.Fatal(err)
			}
			if actor != "operator@example.com" || action != "run.start" {
				t.Fatalf("wrong audit entry: %s %s", actor, action)
			}
		})
	}
}

// TestStartRun_ignoresBranchFromReports checks that a report can't choose what an operator's click runs.
func TestStartRun_ignoresBranchFromReports(t *testing.T) {
	db := testDB(t)
	var body string
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer platform.Close()
	cfg := testConfig()
	cfg.Triggers.GitHubToken, cfg.Triggers.GitHubAPIURL, cfg.Triggers.Ref = "tok", platform.URL, "trunk"
	s := testServer(t, cfg, db)

	forged := testReport("acme/infra")
	forged.Kind, forged.Status = ci.KindApply, ci.StatusApplied
	forged.Run.IsPullRequest, forged.Run.PullRequest = false, ""
	forged.Run.Branch = "attacker-branch"
	if _, err := RecordReport(t.Context(), db, forged); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, signedIn(t, s, "POST", "/api/v1/workspaces/1/runs", "operator@example.com", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("got %d", w.Code)
	}
	if !strings.Contains(body, `"ref":"trunk"`) || strings.Contains(body, "attacker") {
		t.Fatalf("wrong ref: %s", body)
	}
}

func TestStartRun_azureUsesBasicAuth(t *testing.T) {
	var auth string
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer platform.Close()
	config := TriggerConfig{Ref: "main", AzureToken: "pat", AzureOrgURL: platform.URL, AzurePipelines: map[string]string{"Infra Repo": "proj/1"}}
	workspace := Workspace{Platform: ci.PlatformAzure, Repository: "Infra Repo"}
	if !config.supports(workspace) {
		t.Fatal("mixed-case repository names must match")
	}
	if err := config.start(t.Context(), platform.Client(), workspace); err != nil {
		t.Fatal(err)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte(":pat")); auth != want {
		t.Fatalf("got %q, want %q", auth, want)
	}
}

func TestStartRun_signInPageIsAFailure(t *testing.T) {
	// Azure DevOps answers a bad token with 203 and a sign-in page.
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNonAuthoritativeInfo)
	}))
	defer platform.Close()
	config := TriggerConfig{Ref: "main", AzureToken: "bad", AzureOrgURL: platform.URL, AzurePipelines: map[string]string{"r": "p/1"}}
	if err := config.start(t.Context(), platform.Client(), Workspace{Platform: ci.PlatformAzure, Repository: "r"}); err == nil {
		t.Fatal("a 203 must not count as started")
	}
}

func TestScopeAllows(t *testing.T) {
	for _, test := range []struct {
		scope, platform, repository string
		want                        bool
	}{
		{"*", "github", "acme/x", true},
		{"acme/*", "github", "acme/x", true},
		{"acme/*", "github", "other/x", false},
		{"acme/*", "github", "acme/x/y", false},
		{"github:acme/*", "github", "acme/x", true},
		{"github:acme/*", "gitlab", "acme/x", false},
		{"gitlab:*", "gitlab", "any/thing", true},
		{"gitlab:*", "github", "any/thing", false},
	} {
		if got := scopeAllows(test.scope, test.platform, test.repository); got != test.want {
			t.Errorf("scopeAllows(%q, %q, %q) = %t", test.scope, test.platform, test.repository, got)
		}
	}
}

func TestDevUser_onlyAnswersLocally(t *testing.T) {
	db := testDB(t)
	cfg := testConfig()
	cfg.PublicURL = "http://localhost:8080"
	cfg.DevUser = "dev@localhost"
	handler := testServer(t, cfg, db).Handler()
	for host, want := range map[string]int{
		"localhost:8080":   http.StatusOK,
		"127.0.0.1:8080":   http.StatusOK,
		"[::1]:8080":       http.StatusOK,
		"localhost":        http.StatusOK,
		"evil.example.com": http.StatusForbidden,
		"192.168.1.5:8080": http.StatusForbidden,
	} {
		r := httptest.NewRequest("GET", "/api/v1/me", nil)
		r.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: got %d, want %d", host, w.Code, want)
		}
	}
}

func TestStartRun_notConfigured(t *testing.T) {
	db := testDB(t)
	s := testServer(t, testConfig(), db)
	if _, err := RecordReport(t.Context(), db, testReport("acme/infra")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, signedIn(t, s, "POST", "/api/v1/workspaces/1/runs", "operator@example.com", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d", w.Code)
	}
}

func TestStartRun_hidesPlatformErrors(t *testing.T) {
	db := testDB(t)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "internal detail")
	}))
	defer platform.Close()
	cfg := testConfig()
	cfg.Triggers.GitHubToken, cfg.Triggers.GitHubAPIURL = "tok", platform.URL
	s := testServer(t, cfg, db)
	if _, err := RecordReport(t.Context(), db, testReport("acme/infra")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, signedIn(t, s, "POST", "/api/v1/workspaces/1/runs", "operator@example.com", nil))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "internal detail") {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
}

func TestMigrateIsIdempotentAndSafeConcurrently(t *testing.T) {
	db := testDB(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Migrate(context.Background(), db)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// fakeOIDC is an identity provider that follows the protocol, and can misbehave on request.
type fakeOIDC struct {
	*httptest.Server
	key          *rsa.PrivateKey
	email        string
	verified     any
	issuer       string
	audience     string
	nonce        string
	expired      bool
	wrongKey     bool
	gotChallenge string
	gotVerifier  string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: key, email: "operator@example.com", verified: true, audience: "client"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token", "jwks_uri": f.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.gotChallenge = q.Get("code_challenge")
		f.nonce = q.Get("nonce")
		target, _ := url.Parse(q.Get("redirect_uri"))
		values := target.Query()
		values.Set("code", "the-code")
		values.Set("state", q.Get("state"))
		target.RawQuery = values.Encode()
		http.Redirect(w, r, target.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.gotVerifier = r.Form.Get("code_verifier")
		issuer := f.issuer
		if issuer == "" {
			issuer = f.URL
		}
		expires := time.Now().Add(time.Hour)
		if f.expired {
			expires = time.Now().Add(-time.Hour)
		}
		claims := jwt.MapClaims{"iss": issuer, "aud": f.audience, "exp": expires.Unix(), "nonce": f.nonce, "email": f.email, "name": "Test User"}
		if f.verified != nil {
			claims["email_verified"] = f.verified
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = "k1"
		signing := key
		if f.wrongKey {
			signing, _ = rsa.GenerateKey(rand.Reader, 2048)
		}
		signed, err := token.SignedString(signing)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": signed})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// browserSignIn follows the whole sign-in as a browser would, and returns where it ended up.
func browserSignIn(t *testing.T, idp *fakeOIDC, trustEmail bool) (*http.Client, string, *httptest.Server) {
	t.Helper()
	cfg := testConfig()
	cfg.Auth.Issuer = idp.URL
	cfg.Auth.TrustEmail = trustEmail

	db := testDB(t)
	var app *httptest.Server
	s := New(cfg, db)
	s.logger = log.New(io.Discard, "", 0)
	app = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(app.Close)
	s.cfg.PublicURL = app.URL

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Path == "/" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	res, err := client.Get(app.URL + "/api/v1/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return client, res.Header.Get("Location"), app
}

func TestSignIn(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		idp := newFakeOIDC(t)
		client, location, app := browserSignIn(t, idp, false)
		if location != "/" {
			t.Fatalf("redirected to %q", location)
		}
		res, err := client.Get(app.URL + "/api/v1/me")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var me map[string]string
		json.NewDecoder(res.Body).Decode(&me)
		if me["email"] != "operator@example.com" || me["role"] != RoleOperator || me["name"] != "Test User" {
			t.Fatalf("wrong identity: %v", me)
		}
		if idp.gotChallenge == "" || idp.gotVerifier == "" {
			t.Fatal("PKCE wasn't used")
		}
	})

	failures := map[string]struct {
		change func(*fakeOIDC)
		want   string
	}{
		"not on the allowlist": {func(f *fakeOIDC) { f.email = "stranger@example.com" }, "not-allowed"},
		"unverified email":     {func(f *fakeOIDC) { f.verified = false }, "failed"},
		"no verified claim":    {func(f *fakeOIDC) { f.verified = nil }, "failed"},
		"another audience":     {func(f *fakeOIDC) { f.audience = "someone-else" }, "failed"},
		"another issuer":       {func(f *fakeOIDC) { f.issuer = "https://evil.example.com" }, "failed"},
		"expired token":        {func(f *fakeOIDC) { f.expired = true }, "failed"},
		"forged signature":     {func(f *fakeOIDC) { f.wrongKey = true }, "failed"},
		"no email":             {func(f *fakeOIDC) { f.email = "" }, "failed"},
	}
	for name, test := range failures {
		t.Run(name, func(t *testing.T) {
			idp := newFakeOIDC(t)
			test.change(idp)
			client, location, app := browserSignIn(t, idp, false)
			if location != "/?auth_error="+test.want {
				t.Fatalf("redirected to %q", location)
			}
			res, err := client.Get(app.URL + "/api/v1/me")
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("should not be signed in: %d", res.StatusCode)
			}
		})
	}

	t.Run("wrong nonce", func(t *testing.T) {
		idp := newFakeOIDC(t)
		original := idp.Config.Handler
		idp.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/token" {
				idp.nonce = "replayed"
			}
			original.ServeHTTP(w, r)
		})
		_, location, _ := browserSignIn(t, idp, false)
		if location != "/?auth_error=failed" {
			t.Fatalf("redirected to %q", location)
		}
	})

	t.Run("trusted email without the claim", func(t *testing.T) {
		idp := newFakeOIDC(t)
		idp.verified = nil
		if _, location, _ := browserSignIn(t, idp, true); location != "/" {
			t.Fatalf("redirected to %q", location)
		}
	})
}

func TestCallback_rejectsForgedState(t *testing.T) {
	s := testServer(t, testConfig(), nil)
	value, _ := s.sign(loginCookie, loginData{State: "right", Nonce: "n", Verifier: "v", Expires: time.Now().Add(time.Minute).Unix()})
	for _, test := range []struct {
		name, cookie, query, want string
	}{
		{"wrong state", value, "state=wrong&code=c", "invalid"},
		{"no cookie", "", "state=right&code=c", "expired"},
		{"forged cookie", value + "x", "state=right&code=c", "expired"},
		{"provider error", value, "state=right&error=access_denied", "denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/auth/callback?"+test.query, nil)
			if test.cookie != "" {
				r.AddCookie(&http.Cookie{Name: s.cookieName(loginCookie), Value: test.cookie})
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if got := w.Header().Get("Location"); got != "/?auth_error="+test.want {
				t.Fatalf("redirected to %q", got)
			}
		})
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	s := testServer(t, testConfig(), nil)
	w := httptest.NewRecorder()
	s.setCookie(w, sessionCookie, "value", time.Hour)
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "__Host-bogform_session" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("weak cookie: %+v", cookie)
	}
}

func TestGitHubSignIn(t *testing.T) {
	mux := http.NewServeMux()
	verified := true
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho", "token_type": "bearer"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"login": "octo"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"email": "other@example.com", "primary": false, "verified": true},
			{"email": "octo@example.com", "primary": true, "verified": verified},
		})
	})
	github := httptest.NewServer(mux)
	defer github.Close()

	auth := &githubAuthenticator{cfg: AuthConfig{ClientID: "id", ClientSecret: "s", GitHubURL: github.URL, GitHubAPIURL: github.URL}, client: github.Client()}
	target, err := auth.AuthCodeURL(t.Context(), "https://app/cb", "st", "", "verifier")
	if err != nil || !strings.Contains(target, "code_challenge=") || !strings.Contains(target, "state=st") {
		t.Fatalf("wrong URL %q: %v", target, err)
	}
	identity, err := auth.Exchange(t.Context(), "https://app/cb", "code", "", "verifier")
	if err != nil || identity.Email != "octo@example.com" || identity.Name != "octo" {
		t.Fatalf("wrong identity %+v: %v", identity, err)
	}
	verified = false
	if _, err := auth.Exchange(t.Context(), "https://app/cb", "code", "", "verifier"); err == nil {
		t.Fatal("an unverified address must not be accepted")
	}
}

func TestLambdaEvent(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cookie, _ := r.Cookie("a")
		http.SetCookie(w, &http.Cookie{Name: "one", Value: "1"})
		http.SetCookie(w, &http.Cookie{Name: "two", Value: "2"})
		w.Header().Set("X-Seen", r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(body)+" "+cookie.Value+" "+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, "done")
	})
	event, _ := json.Marshal(map[string]any{
		"rawPath": "/api/v1/reports", "rawQueryString": "x=1",
		"cookies": []string{"a=b"}, "headers": map[string]string{"authorization": "Bearer t"},
		"body": base64.StdEncoding.EncodeToString([]byte("payload")), "isBase64Encoded": true,
		"requestContext": map[string]any{"http": map[string]string{"method": "POST"}},
	})
	raw, err := handleLambdaEvent(t.Context(), handler, event)
	if err != nil {
		t.Fatal(err)
	}
	var response functionURLResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated || response.Body != "done" || len(response.Cookies) != 2 {
		t.Fatalf("wrong response: %+v", response)
	}
	if got := response.Headers["X-Seen"]; got != "POST /api/v1/reports?x=1 payload b Bearer t" {
		t.Fatalf("request wasn't reconstructed: %q", got)
	}
}

func TestRunLambda(t *testing.T) {
	event, _ := json.Marshal(map[string]any{"rawPath": "/hello", "requestContext": map[string]any{"http": map[string]string{"method": "GET"}}})
	responses := make(chan string, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := false
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/next") && !served:
			served = true
			w.Header().Set("Lambda-Runtime-Aws-Request-Id", "req-1")
			w.Header().Set("Lambda-Runtime-Deadline-Ms", fmt.Sprint(time.Now().Add(time.Minute).UnixMilli()))
			w.Write(event)
		case strings.HasSuffix(r.URL.Path, "/next"):
			cancel()
			<-r.Context().Done()
		case strings.HasSuffix(r.URL.Path, "/req-1/response"):
			raw, _ := io.ReadAll(r.Body)
			responses <- string(raw)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer runtime.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from "+r.URL.Path) })
	err := RunLambda(ctx, strings.TrimPrefix(runtime.URL, "http://"), handler)
	if err == nil {
		t.Fatal("expected the loop to end when the context ended")
	}
	if got := <-responses; !strings.Contains(got, "hello from /hello") {
		t.Fatalf("wrong response: %s", got)
	}
}
