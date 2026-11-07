// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/bogware/bogform/internal/ci"
	"github.com/bogware/bogform/internal/dashboard"
)

// The overview's types are shared with "bogform ui".
type (
	RunSummary         = dashboard.RunSummary
	WorkspaceOverview  = dashboard.WorkspaceOverview
	RepositoryOverview = dashboard.RepositoryOverview
)

func groupRepositories(workspaces []WorkspaceOverview) []RepositoryOverview {
	return dashboard.GroupRepositories(workspaces)
}

// overviewLimit bounds the overview's size. A single organization's dashboard is far below it.
const overviewLimit = 2000

// loadWorkspaceOverviews reads every workspace with its latest run of each kind, in one query.
func loadWorkspaceOverviews(ctx context.Context, db *sql.DB) ([]WorkspaceOverview, error) {
	var columns []string
	for _, column := range strings.Split(workspaceColumns, ",") {
		columns = append(columns, "w."+strings.TrimSpace(column))
	}
	latest := func(alias, kind string) string {
		return `LEFT JOIN LATERAL (SELECT id, status, finished_at, pull_request, counts FROM runs
			WHERE workspace_id = w.id AND kind = '` + kind + `' ORDER BY finished_at DESC, id DESC LIMIT 1) ` + alias + ` ON TRUE`
	}
	rows, err := db.QueryContext(ctx, `SELECT `+strings.Join(columns, ", ")+`,
		p.id, p.status, p.finished_at, p.pull_request, p.counts,
		a.id, a.status, a.finished_at, a.pull_request, a.counts,
		d.id, d.status, d.finished_at, d.pull_request, d.counts
		FROM workspaces w `+latest("p", ci.KindPlan)+` `+latest("a", ci.KindApply)+` `+latest("d", ci.KindDrift)+`
		ORDER BY w.repository, w.directory, w.name LIMIT $1`, overviewLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []WorkspaceOverview
	for rows.Next() {
		var item WorkspaceOverview
		var runs [3]nullRun
		w := &item.Workspace
		if err := rows.Scan(&w.ID, &w.Platform, &w.Repository, &w.Directory, &w.Name, &w.LastStatus, &w.LastKind,
			&w.LastRunAt, &w.LastApplyAt, &w.DriftStatus, &w.DriftCheckAt,
			&runs[0].id, &runs[0].status, &runs[0].finishedAt, &runs[0].pullRequest, &runs[0].counts,
			&runs[1].id, &runs[1].status, &runs[1].finishedAt, &runs[1].pullRequest, &runs[1].counts,
			&runs[2].id, &runs[2].status, &runs[2].finishedAt, &runs[2].pullRequest, &runs[2].counts,
		); err != nil {
			return nil, err
		}
		for i, target := range []**RunSummary{&item.LastPlan, &item.LastApply, &item.LastDrift} {
			summary, err := runs[i].summary()
			if err != nil {
				return nil, err
			}
			*target = summary
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type nullRun struct {
	id          sql.NullInt64
	status      sql.NullString
	finishedAt  sql.NullTime
	pullRequest sql.NullString
	counts      []byte
}

func (n nullRun) summary() (*RunSummary, error) {
	if !n.id.Valid {
		return nil, nil
	}
	summary := &RunSummary{ID: n.id.Int64, Status: n.status.String, FinishedAt: n.finishedAt.Time, PullRequest: n.pullRequest.String}
	if len(n.counts) > 0 {
		if err := json.Unmarshal(n.counts, &summary.Counts); err != nil {
			return nil, err
		}
	}
	return summary, nil
}
