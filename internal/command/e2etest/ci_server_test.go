// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package e2etest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bogware/bogform/internal/ci"
	"github.com/bogware/bogform/internal/server"
)

// TestCI_reportsToServer connects the real CLI to the real dashboard server and a real database.
func TestCI_reportsToServer(t *testing.T) {
	dsn := os.Getenv("BOG_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BOG_TEST_DATABASE_URL isn't set")
	}
	db, err := server.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := server.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `TRUNCATE ingest_tokens, workspaces, runs, audit_log RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	token, err := server.CreateToken(t.Context(), db, "e2e", "acme/*")
	if err != nil {
		t.Fatal(err)
	}

	cfg := server.Config{
		PublicURL: "http://localhost", DatabaseURL: "unused", SessionKey: bytes.Repeat([]byte{1}, 32), DevUser: "dev@localhost",
	}
	app := httptest.NewServer(server.New(cfg, db).Handler())
	defer app.Close()

	get := func(path string) string {
		t.Helper()
		res, err := http.Get(app.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, res.StatusCode, body)
		}
		return string(body)
	}

	dir := t.TempDir()
	writeCIConfig(t, dir, ciConfig)
	env, _ := ciEnvironment(t, "http://127.0.0.1:1", "push")
	env = append(env, "BOG_SERVER_URL="+app.URL, "BOG_SERVER_TOKEN="+token)

	result := runCI(t, dir, env, "plan")
	if result.exit != 0 {
		t.Fatalf("plan failed: %s\n%s", result.stdout, result.stderr)
	}
	if strings.Contains(result.stderr, "Warning") {
		t.Fatalf("unexpected warning: %s", result.stderr)
	}
	result = runCI(t, dir, env, "apply")
	if result.exit != 0 {
		t.Fatalf("apply failed: %s\n%s", result.stdout, result.stderr)
	}
	result = runCI(t, dir, env, "drift")
	if result.exit != 0 {
		t.Fatalf("drift failed: %s\n%s", result.stdout, result.stderr)
	}

	var overview struct {
		Workspaces int `json:"workspaces"`
		RecentRuns []struct {
			ID         int64  `json:"id"`
			Kind       string `json:"kind"`
			Status     string `json:"status"`
			Repository string `json:"repository"`
			Commit     string `json:"commit"`
		} `json:"recent_runs"`
	}
	if err := json.Unmarshal([]byte(get("/api/v1/overview")), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Workspaces != 1 || len(overview.RecentRuns) != 3 {
		t.Fatalf("wrong overview: %+v", overview)
	}
	if got := overview.RecentRuns[1]; got.Kind != ci.KindApply || got.Status != ci.StatusApplied || got.Repository != "acme/infra" || got.Commit != "0123456789abcdef" {
		t.Fatalf("wrong run: %+v", got)
	}

	all := get("/api/v1/overview") + get("/api/v1/workspaces") + get("/api/v1/workspaces/1/runs") + get("/api/v1/runs/1")
	if strings.Contains(all, "hunter2") || strings.Contains(all, token) {
		t.Fatalf("the server stored a secret:\n%s", all)
	}
	if !strings.Contains(get("/api/v1/runs/1"), "terraform_data.example") {
		t.Fatal("the run should list its resources")
	}

	t.Run("a token outside its scope only produces a warning", func(t *testing.T) {
		other := append(append([]string{}, env...), "GITHUB_REPOSITORY=other/infra")
		dir := t.TempDir()
		writeCIConfig(t, dir, ciConfig)
		result := runCI(t, dir, other, "plan")
		if result.exit != 0 {
			t.Fatalf("a server problem must not fail the pipeline: %s", result.stderr)
		}
		if !strings.Contains(result.stderr, "Warning: couldn't send the report") {
			t.Fatalf("expected a warning: %s", result.stderr)
		}
	})

	t.Run("the server's token isn't visible to the configuration", func(t *testing.T) {
		dir := t.TempDir()
		writeCIConfig(t, dir, `output "visible" { value = try(file("/proc/self/environ"), "") }`)
		result := runCI(t, dir, env, "plan", "-no-comment")
		if strings.Contains(result.stdout, token) {
			t.Fatal("the token reached the plan's environment")
		}
	})

	t.Run("a plain http server elsewhere is refused", func(t *testing.T) {
		insecure := append(append([]string{}, env...), "BOG_SERVER_URL=http://bogform.example.com")
		dir := t.TempDir()
		writeCIConfig(t, dir, ciConfig)
		result := runCI(t, dir, insecure, "plan")
		if result.exit != 0 || !strings.Contains(result.stderr, "must use https") {
			t.Fatalf("expected a warning and success: %d\n%s", result.exit, result.stderr)
		}
	})
}
