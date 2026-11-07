// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package dashboard holds what the dashboard shows, shared by the self-hosted server and the local
// "bogform ui", so that both serve the same API to the same web page.
package dashboard

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

// Workspace is one configuration directory and workspace in one repository.
type Workspace struct {
	ID           int64      `json:"id"`
	Platform     string     `json:"platform"`
	Repository   string     `json:"repository"`
	Directory    string     `json:"directory"`
	Name         string     `json:"workspace"`
	DefaultRef   string     `json:"default_ref,omitempty"`
	LastStatus   string     `json:"last_status"`
	LastKind     string     `json:"last_kind"`
	LastRunAt    time.Time  `json:"last_run_at"`
	LastApplyAt  *time.Time `json:"last_apply_at,omitempty"`
	DriftStatus  string     `json:"drift_status,omitempty"`
	DriftCheckAt *time.Time `json:"drift_checked_at,omitempty"`
}

// Run is the record of one pipeline run.
type Run struct {
	ID             int64       `json:"id"`
	WorkspaceID    int64       `json:"workspace_id"`
	Repository     string      `json:"repository,omitempty"`
	Directory      string      `json:"directory,omitempty"`
	WorkspaceName  string      `json:"workspace,omitempty"`
	Kind           string      `json:"kind"`
	Status         string      `json:"status"`
	Branch         string      `json:"branch"`
	Commit         string      `json:"commit"`
	PullRequest    string      `json:"pull_request,omitempty"`
	RunURL         string      `json:"run_url,omitempty"`
	BogformVersion string      `json:"bogform_version"`
	Counts         ci.Counts   `json:"counts"`
	Changes        []ci.Change `json:"changes,omitempty"`
	OutputsChanged []string    `json:"outputs_changed,omitempty"`
	Error          string      `json:"error,omitempty"`
	StartedAt      time.Time   `json:"started_at"`
	FinishedAt     time.Time   `json:"finished_at"`
}

// RunSummary is the gist of one run, for the overview.
type RunSummary struct {
	ID          int64     `json:"id"`
	Status      string    `json:"status"`
	FinishedAt  time.Time `json:"finished_at"`
	PullRequest string    `json:"pull_request,omitempty"`
	Counts      ci.Counts `json:"counts"`
}

// WorkspaceOverview is a workspace with its latest run of each kind.
type WorkspaceOverview struct {
	Workspace
	LastPlan  *RunSummary `json:"last_plan,omitempty"`
	LastApply *RunSummary `json:"last_apply,omitempty"`
	LastDrift *RunSummary `json:"last_drift,omitempty"`
	// OpenChanges are the changes in the latest plan, if it's newer than the latest apply.
	OpenChanges *ci.Counts `json:"open_changes,omitempty"`
	Severity    int        `json:"-"`
}

// RepositoryOverview groups a repository's workspaces.
type RepositoryOverview struct {
	Platform   string              `json:"platform"`
	Repository string              `json:"repository"`
	Workspaces []WorkspaceOverview `json:"workspaces"`
}

// Severity levels, most urgent first.
const (
	severityFailed = iota
	severityDrifted
	severityPending
	severityOK
)

// Classify works out the workspace's open changes and how urgent it is.
func (w *WorkspaceOverview) Classify() {
	w.OpenChanges = nil
	if plan := w.LastPlan; plan != nil && plan.Status == ci.StatusChanges && plan.Counts.Total() > 0 &&
		(w.LastApply == nil || plan.FinishedAt.After(w.LastApply.FinishedAt)) {
		counts := plan.Counts
		w.OpenChanges = &counts
	}
	switch {
	case w.LastStatus == ci.StatusFailed || (w.LastApply != nil && w.LastApply.Status == ci.StatusFailed):
		w.Severity = severityFailed
	case w.DriftStatus == ci.StatusDrifted:
		w.Severity = severityDrifted
	case w.OpenChanges != nil:
		w.Severity = severityPending
	default:
		w.Severity = severityOK
	}
}

// GroupRepositories sorts workspaces into repositories, with failures first, then drift, then
// unapplied changes, so that what needs attention is at the top.
func GroupRepositories(workspaces []WorkspaceOverview) []RepositoryOverview {
	type key struct{ platform, repository string }
	index := map[key]int{}
	var repositories []RepositoryOverview
	severity := map[key]int{}
	for _, workspace := range workspaces {
		workspace.Classify()
		k := key{workspace.Platform, workspace.Repository}
		i, found := index[k]
		if !found {
			i = len(repositories)
			index[k] = i
			repositories = append(repositories, RepositoryOverview{Platform: k.platform, Repository: k.repository})
			severity[k] = severityOK
		}
		repositories[i].Workspaces = append(repositories[i].Workspaces, workspace)
		severity[k] = min(severity[k], workspace.Severity)
	}
	for i := range repositories {
		slices.SortFunc(repositories[i].Workspaces, func(a, b WorkspaceOverview) int {
			return cmp.Or(cmp.Compare(a.Severity, b.Severity), strings.Compare(a.Directory, b.Directory), strings.Compare(a.Name, b.Name))
		})
	}
	slices.SortFunc(repositories, func(a, b RepositoryOverview) int {
		return cmp.Or(
			cmp.Compare(severity[key{a.Platform, a.Repository}], severity[key{b.Platform, b.Repository}]),
			strings.Compare(a.Repository, b.Repository),
			strings.Compare(a.Platform, b.Platform))
	})
	if repositories == nil {
		repositories = []RepositoryOverview{}
	}
	return repositories
}
