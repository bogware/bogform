// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package tfcmigrate

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T, downloadAuth *string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer secret-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v2/organizations/acme/workspaces/web", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"id":"ws-1","attributes":{"name":"web","terraform-version":"1.9.5","working-directory":"infra/","execution-mode":"remote","auto-apply":true,"vcs-repo":{"identifier":"acme/web","branch":""}}}}`))
	}))
	mux.HandleFunc("/api/v2/organizations/acme/workspaces/empty", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"id":"ws-2","attributes":{"name":"empty"}}}`))
	}))
	mux.HandleFunc("/api/v2/workspaces/ws-1/vars", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"var-1","attributes":{"key":"region","value":"us-east-1","category":"terraform"}},{"id":"var-2","attributes":{"key":"db_password","value":null,"category":"terraform","sensitive":true}},{"id":"var-3","attributes":{"key":"AWS_SECRET_ACCESS_KEY","category":"env","sensitive":true}}]}`))
	}))
	mux.HandleFunc("/api/v2/workspaces/ws-1/current-state-version", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"id":"sv-1","attributes":{"serial":7,"hosted-state-download-url":"` + srv.URL + `/download/sv-1"}}}`))
	}))
	mux.HandleFunc("/api/v2/workspaces/ws-2/current-state-version", auth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	mux.HandleFunc("/download/sv-1", func(w http.ResponseWriter, r *http.Request) {
		*downloadAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"version":4,"serial":7,"lineage":"abc"}`))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient(t *testing.T) {
	var downloadAuth string
	srv := testServer(t, &downloadAuth)
	c := &Client{BaseURL: srv.URL + "/api/v2", Token: "secret-token"}
	ctx := context.Background()

	ws, err := c.Workspace(ctx, "acme", "web")
	if err != nil {
		t.Fatal(err)
	}
	if ws.ID != "ws-1" || !ws.AutoApply || ws.VCSRepository != "acme/web" || ws.WorkingDirectory != "infra/" {
		t.Fatalf("unexpected workspace: %+v", ws)
	}

	vars, err := c.Variables(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 3 || vars[0].Value != "us-east-1" || !vars[1].Sensitive || vars[1].Value != "" {
		t.Fatalf("unexpected variables: %+v", vars)
	}

	sv, err := c.CurrentStateVersion(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := c.DownloadState(ctx, sv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"lineage":"abc"`)) {
		t.Fatalf("unexpected state: %s", data)
	}
	// The test server shares the API's host, so the token is sent. See TestTokenNotSentElsewhere.
	if downloadAuth == "" {
		t.Fatal("expected the token on a same-host download")
	}

	empty, err := c.Workspace(ctx, "acme", "empty")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CurrentStateVersion(ctx, empty.ID); !errors.Is(err, ErrNoState) {
		t.Fatalf("expected ErrNoState, got %v", err)
	}
}

func TestClientErrors(t *testing.T) {
	var downloadAuth string
	srv := testServer(t, &downloadAuth)
	ctx := context.Background()

	bad := &Client{BaseURL: srv.URL + "/api/v2", Token: "wrong"}
	if _, err := bad.Workspace(ctx, "acme", "web"); err == nil || !strings.Contains(err.Error(), "rejected the token") {
		t.Fatalf("expected a token error, got %v", err)
	}
	c := &Client{BaseURL: srv.URL + "/api/v2", Token: "secret-token"}
	if _, err := c.Workspace(ctx, "acme", "missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found, got %v", err)
	}
	if strings.Contains(c.BaseURL, "secret-token") {
		t.Fatal("token leaked into the URL")
	}
}

func TestTokenNotSentElsewhere(t *testing.T) {
	var gotAuth string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer other.Close()
	c := &Client{BaseURL: "http://127.0.0.2:1/api/v2", Token: "secret-token"}
	if _, err := c.DownloadState(context.Background(), &StateVersion{DownloadURL: other.URL + "/x"}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Fatalf("the token was sent to another host: %q", gotAuth)
	}
}

func TestReport(t *testing.T) {
	ws := &Workspace{Name: "web", WorkingDirectory: "infra/", AutoApply: true, VCSRepository: "acme/web", VCSBranch: "main"}
	vars := []Variable{
		{Key: "region", Value: "us-east-1", Category: "terraform"},
		{Key: "db_password", Category: "terraform", Sensitive: true},
		{Key: "AWS_SECRET_ACCESS_KEY", Category: "env", Sensitive: true},
		{Key: "contact", Value: "someone@example.com", Category: "terraform"},
	}
	var hidden, shown bytes.Buffer
	WriteReport(&hidden, ws, vars, false)
	WriteReport(&shown, ws, vars, true)

	if strings.Contains(hidden.String(), "us-east-1") || strings.Contains(hidden.String(), "someone@example.com") {
		t.Fatalf("values shown without -show-values:\n%s", hidden.String())
	}
	if !strings.Contains(shown.String(), "region") || !strings.Contains(shown.String(), "= us-east-1") {
		t.Fatalf("values missing with -show-values:\n%s", shown.String())
	}
	for _, want := range []string{
		"bogform ci init -directory=infra -apply-on=merge -branch=main",
		"secret DB_PASSWORD -> TF_VAR_db_password",
		"Don't copy these",
		"disconnect acme/web",
	} {
		if !strings.Contains(hidden.String(), want) {
			t.Errorf("report is missing %q:\n%s", want, hidden.String())
		}
	}
	// AWS keys are replaced by OIDC, so they shouldn't be suggested as secrets.
	if strings.Contains(hidden.String(), "secret AWS_SECRET_ACCESS_KEY") {
		t.Errorf("cloud credentials suggested as a secret:\n%s", hidden.String())
	}
}
