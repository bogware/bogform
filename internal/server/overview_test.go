// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

func TestGroupRepositories(t *testing.T) {
	at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	ws := func(repository, directory, status, drift string, plan, apply *RunSummary) WorkspaceOverview {
		return WorkspaceOverview{
			Workspace: Workspace{Platform: ci.PlatformGitHub, Repository: repository, Directory: directory, Name: "default", LastStatus: status, DriftStatus: drift},
			LastPlan:  plan, LastApply: apply,
		}
	}
	pending := &RunSummary{Status: ci.StatusChanges, FinishedAt: at, Counts: ci.Counts{Add: 2}}
	applied := &RunSummary{Status: ci.StatusApplied, FinishedAt: at.Add(-time.Hour)}
	appliedLater := &RunSummary{Status: ci.StatusApplied, FinishedAt: at.Add(time.Hour)}
	failedApply := &RunSummary{Status: ci.StatusFailed, FinishedAt: at.Add(time.Hour)}

	got := groupRepositories([]WorkspaceOverview{
		ws("acme/web", "infra", ci.StatusNoChanges, "", nil, nil),
		ws("acme/api", "infra", ci.StatusApplied, "", pending, appliedLater),   // Plan is older than the apply.
		ws("acme/api", "dns", ci.StatusChanges, "", pending, applied),          // Unapplied changes.
		ws("acme/data", "infra", ci.StatusApplied, ci.StatusDrifted, nil, nil), // Drifted.
		ws("acme/zeta", "infra", ci.StatusChanges, "", nil, failedApply),       // Latest apply failed.
	})
	var order []string
	for _, repository := range got {
		order = append(order, repository.Repository)
	}
	if want := []string{"acme/zeta", "acme/data", "acme/api", "acme/web"}; len(order) != len(want) || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("got order %v, want %v", order, want)
	}
	api := got[2].Workspaces
	if api[0].Directory != "dns" || api[0].OpenChanges == nil || api[0].OpenChanges.Add != 2 {
		t.Errorf("unapplied changes should come first, with their counts: %+v", api[0])
	}
	if api[1].OpenChanges != nil {
		t.Errorf("a plan older than the latest apply has nothing open: %+v", api[1])
	}
	if len(groupRepositories(nil)) != 0 || groupRepositories(nil) == nil {
		t.Error("no workspaces should give an empty list, not null")
	}
}

func TestOverview(t *testing.T) {
	db := testDB(t)
	s := testServer(t, testConfig(), db)
	ctx := t.Context()
	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	record := func(repository, directory, kind, status string, minutes int, pullRequest bool) {
		t.Helper()
		report := testReport(repository)
		report.Directory, report.Kind, report.Status = directory, kind, status
		report.Run.IsPullRequest = pullRequest
		report.StartedAt = at.Add(time.Duration(minutes) * time.Minute).Add(-time.Second)
		report.FinishedAt = at.Add(time.Duration(minutes) * time.Minute)
		if _, err := RecordReport(ctx, db, report); err != nil {
			t.Fatal(err)
		}
	}
	record("acme/infra", "network", ci.KindApply, ci.StatusApplied, 1, false)
	record("acme/infra", "network", ci.KindPlan, ci.StatusChanges, 2, true)
	record("acme/infra", "network", ci.KindDrift, ci.StatusNoChanges, 3, false)
	record("acme/infra", "dns", ci.KindApply, ci.StatusFailed, 4, false)
	record("acme/site", "infra", ci.KindPlan, ci.StatusNoChanges, 5, true)

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, signedIn(t, s, http.MethodGet, "/api/v1/overview", "viewer@example.com", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var overview struct {
		Workspaces   int                  `json:"workspaces"`
		Repositories []RepositoryOverview `json:"repositories"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Workspaces != 3 || len(overview.Repositories) != 2 {
		t.Fatalf("unexpected overview: %s", w.Body)
	}
	infra := overview.Repositories[0]
	if infra.Repository != "acme/infra" || len(infra.Workspaces) != 2 {
		t.Fatalf("the failing repository should be first: %+v", overview.Repositories)
	}
	dns, network := infra.Workspaces[0], infra.Workspaces[1]
	if dns.Directory != "dns" || dns.LastApply == nil || dns.LastApply.Status != ci.StatusFailed {
		t.Errorf("unexpected dns workspace: %+v", dns)
	}
	if network.LastPlan == nil || network.LastPlan.PullRequest != "4" || network.LastApply == nil || network.LastDrift == nil {
		t.Errorf("network should have a run of each kind: %+v", network)
	}
	if network.OpenChanges == nil || network.OpenChanges.Add != 1 {
		t.Errorf("network's plan is newer than its apply, so it has open changes: %+v", network)
	}
	if site := overview.Repositories[1].Workspaces[0]; site.LastApply != nil || site.LastPlan == nil {
		t.Errorf("unexpected site workspace: %+v", site)
	}
}
