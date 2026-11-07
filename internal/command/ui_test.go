// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

func TestRunUI_refusesNetworkAddresses(t *testing.T) {
	dir := t.TempDir()
	for _, address := range []string{"0.0.0.0:0", "example.com:80", ":8080", "nonsense"} {
		if err := runUI(t.Context(), []string{dir}, address, nil, io.Discard); err == nil {
			t.Errorf("%q should be refused", address)
		}
	}
	if err := runUI(t.Context(), []string{filepath.Join(dir, "missing")}, "127.0.0.1:0", nil, io.Discard); err == nil {
		t.Error("a missing directory should be refused")
	}
}

func TestRunUI(t *testing.T) {
	dir := t.TempDir()
	report := ci.Report{
		FormatVersion: ci.ReportFormatVersion, Kind: ci.KindPlan, Status: ci.StatusChanges, Directory: "infra", Workspace: "default",
		Run:       ci.Context{Platform: ci.PlatformGitHub, Repository: "acme/infra"},
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(),
	}
	raw, _ := json.Marshal(report)
	if err := os.MkdirAll(filepath.Join(dir, "infra", ".bogform-ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "infra", ".bogform-ci", "report.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opened := make(chan string, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- runUI(ctx, []string{dir}, "127.0.0.1:0", func(url string) error { opened <- url; return nil }, io.Discard)
	}()

	var url string
	select {
	case url = <-opened:
	case err := <-finished:
		t.Fatalf("stopped early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the browser wasn't opened")
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "?token=") {
		t.Fatalf("unexpected address %q", url)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	if res, err := client.Get(url); err != nil {
		t.Fatal(err)
	} else {
		res.Body.Close()
	}
	base := strings.SplitN(url, "/?", 2)[0]
	res, err := client.Get(base + "/api/v1/overview")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `"repository":"acme/infra"`) {
		t.Errorf("unexpected overview: %d %s", res.StatusCode, body)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("stopping: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("didn't stop")
	}
}
