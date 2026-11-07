// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/ci"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open connects to Postgres. Small limits suit serverless hosting, where each instance handles one
// request at a time and low-cost databases allow few connections.
func Open(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, errors.New("couldn't read the database connection string")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(10 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)
	return db, nil
}

// migrationLockID is an arbitrary number identifying this application's migration lock.
const migrationLockID = 7267342817

// Migrate brings the schema up to date. A lock makes it safe when several instances start at once.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // This does nothing after a commit.

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	var current int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %q", name)
		}
		if version <= current {
			continue
		}
		script, err := migrationFiles.ReadFile(path.Join("migrations", name))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("migration %s failed: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// tokenPrefix makes tokens recognizable, for example to secret scanners.
const tokenPrefix = "bogform_"

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// CreateToken stores a new ingest token and returns it. Only its hash is kept, so it can't be shown
// again. The scope limits which repositories it can report for, so a token leaked from one repository
// can't write records for another.
func CreateToken(ctx context.Context, db *sql.DB, name, scope string) (string, error) {
	if name == "" || scope == "" {
		return "", errors.New("a token needs a name and a repository scope")
	}
	pattern := scope
	if _, rest, found := strings.Cut(scope, ":"); found {
		pattern = rest
	}
	if _, err := path.Match(pattern, "x"); err != nil {
		return "", fmt.Errorf("the scope %q isn't a valid pattern", scope)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	_, err := db.ExecContext(ctx, `INSERT INTO ingest_tokens (name, repository_scope, token_hash) VALUES ($1, $2, $3)`, name, scope, hashToken(token))
	if err != nil {
		return "", fmt.Errorf("couldn't create the token: %w", err)
	}
	return token, nil
}

// TokenInfo describes an ingest token.
type TokenInfo struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"repository_scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// ListTokens returns all ingest tokens, without their values, which aren't stored.
func ListTokens(ctx context.Context, db *sql.DB) ([]TokenInfo, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, repository_scope, created_at, last_used_at, revoked_at FROM ingest_tokens ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []TokenInfo{}
	for rows.Next() {
		var token TokenInfo
		if err := rows.Scan(&token.ID, &token.Name, &token.Scope, &token.CreatedAt, &token.LastUsedAt, &token.RevokedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeToken stops a token from working.
func RevokeToken(ctx context.Context, db *sql.DB, name string) error {
	result, err := db.ExecContext(ctx, `UPDATE ingest_tokens SET revoked_at = now() WHERE name = $1 AND revoked_at IS NULL`, name)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("there's no active token named %q", name)
	}
	return nil
}

// tokenScope returns the repository scope of a valid token.
func tokenScope(ctx context.Context, db *sql.DB, token string) (string, bool, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return "", false, nil
	}
	var scope string
	err := db.QueryRowContext(ctx, `UPDATE ingest_tokens SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL RETURNING repository_scope`, hashToken(token)).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return scope, err == nil, err
}

// scopeAllows reports whether a token's scope covers a repository on a platform. Patterns use path.Match,
// so "acme/*" covers every repository directly under acme. A scope such as "github:acme/*" also limits the
// platform, so that the token can't create records that would start pipelines on another platform.
func scopeAllows(scope, platform, repository string) bool {
	if prefix, rest, found := strings.Cut(scope, ":"); found {
		if prefix != platform {
			return false
		}
		scope = rest
	}
	if scope == "*" {
		return true
	}
	matched, err := path.Match(scope, repository)
	return err == nil && matched
}

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

const workspaceColumns = `id, platform, repository, directory, name, last_status, last_kind,
	last_run_at, last_apply_at, drift_status, drift_checked_at`

func scanWorkspace(row interface{ Scan(...any) error }) (Workspace, error) {
	var w Workspace
	err := row.Scan(&w.ID, &w.Platform, &w.Repository, &w.Directory, &w.Name, &w.LastStatus, &w.LastKind,
		&w.LastRunAt, &w.LastApplyAt, &w.DriftStatus, &w.DriftCheckAt)
	return w, err
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

const (
	maxFieldLength = 1024
	maxErrorLength = 4096
	maxChanges     = 5000
)

var (
	validCommit = regexp.MustCompile(`^[0-9a-zA-Z]*$`)
	// validRepository allows what the platforms allow, including spaces in Azure repository names, and
	// nothing that means something in a URL.
	validRepository = regexp.MustCompile(`^[A-Za-z0-9._ -]+(/[A-Za-z0-9._ -]+)*$`)
)

// maxClockSkew is how far in the future a report's timestamps can be.
const maxClockSkew = time.Hour

func validRepositoryName(name string) bool {
	if !validRepository.MatchString(name) {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.Trim(segment, ". ") == "" {
			return false
		}
	}
	return true
}

// validateReport rejects reports that aren't well formed. Pipelines are partly trusted: a token holder
// could send anything, and these values are shown to other people.
func validateReport(report *ci.Report, now time.Time) error {
	if report.FormatVersion != ci.ReportFormatVersion {
		return fmt.Errorf("unsupported report format version %d", report.FormatVersion)
	}
	if !slices.Contains([]string{ci.KindPlan, ci.KindApply, ci.KindDrift}, report.Kind) {
		return fmt.Errorf("unknown kind %q", report.Kind)
	}
	if !slices.Contains([]string{ci.StatusNoChanges, ci.StatusChanges, ci.StatusApplied, ci.StatusDrifted, ci.StatusFailed}, report.Status) {
		return fmt.Errorf("unknown status %q", report.Status)
	}
	if !slices.Contains([]string{ci.PlatformGitHub, ci.PlatformGitLab, ci.PlatformAzure, ci.PlatformBitbucket}, report.Run.Platform) {
		return fmt.Errorf("reports must come from a supported pipeline platform, not %q", report.Run.Platform)
	}
	if report.Run.Repository == "" || report.Directory == "" || report.Workspace == "" {
		return errors.New("repository, directory, and workspace are required")
	}
	// Repository names become part of the URLs used to start pipelines.
	if !validRepositoryName(report.Run.Repository) {
		return errors.New("the repository name isn't valid")
	}
	for _, value := range []string{report.Run.Repository, report.Directory, report.Workspace, report.Run.Branch, report.Run.PullRequest, report.BogformVersion, report.Run.RunURL, report.Run.Actor, report.Run.Trigger} {
		if len(value) > maxFieldLength {
			return errors.New("a field is too long")
		}
	}
	if !validCommit.MatchString(report.Run.Commit) || len(report.Run.Commit) > 64 {
		return errors.New("the commit isn't valid")
	}
	// This becomes a link in the dashboard, so a javascript: address must not get through.
	if url := report.Run.RunURL; url != "" && !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return errors.New("the run URL must use http or https")
	}
	if len(report.Changes) > maxChanges {
		report.Changes = report.Changes[:maxChanges]
	}
	if len(report.Error) > maxErrorLength {
		report.Error = report.Error[:maxErrorLength]
	}
	report.Error = strings.ToValidUTF8(report.Error, "")
	if report.StartedAt.IsZero() || report.FinishedAt.IsZero() {
		return errors.New("start and finish times are required")
	}
	// A report from the far future would hold a workspace's status forever, and never be pruned.
	if report.FinishedAt.After(now.Add(maxClockSkew)) || report.StartedAt.After(now.Add(maxClockSkew)) {
		return errors.New("the report's times are in the future")
	}
	return nil
}

// RecordReport stores a report, and updates the workspace's current status.
func RecordReport(ctx context.Context, db *sql.DB, report *ci.Report) (int64, error) {
	recorded, err := recordReport(ctx, db, report)
	return recorded.runID, err
}

// recorded is what storing a report changed.
type recorded struct {
	runID int64
	// previousDrift is the workspace's drift status before this report, for deciding on alerts.
	previousDrift string
}

func recordReport(ctx context.Context, db *sql.DB, report *ci.Report) (recorded, error) {
	var result recorded
	// Status decisions rely on these two fields agreeing, so a report can't claim both at once.
	if !report.Run.IsPullRequest {
		report.Run.PullRequest = ""
	}
	changes, err := json.Marshal(report.Changes)
	if err != nil {
		return result, err
	}
	outputs, err := json.Marshal(report.OutputsChanged)
	if err != nil {
		return result, err
	}
	counts, err := json.Marshal(report.Counts)
	if err != nil {
		return result, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback() //nolint:errcheck // This does nothing after a commit.

	// A pull request's run only sets the status until the first run outside a pull request, because one
	// green plan on a branch mustn't hide a failed deployment.
	// Lock the workspace's row, if it exists, so that the drift status read here is the one replaced.
	err = tx.QueryRowContext(ctx, `SELECT drift_status FROM workspaces WHERE platform = $1 AND repository = $2 AND directory = $3 AND name = $4 FOR UPDATE`,
		report.Run.Platform, report.Run.Repository, report.Directory, report.Workspace).Scan(&result.previousDrift)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}

	var workspaceID int64
	mainline := !report.Run.IsPullRequest
	err = tx.QueryRowContext(ctx, `
		INSERT INTO workspaces (platform, repository, directory, name, last_status, last_kind, status_from_mainline, last_run_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (platform, repository, directory, name) DO UPDATE SET
			status_from_mainline = workspaces.status_from_mainline OR EXCLUDED.status_from_mainline,
			last_run_at = GREATEST(workspaces.last_run_at, EXCLUDED.last_run_at)
		RETURNING id`,
		report.Run.Platform, report.Run.Repository, report.Directory, report.Workspace, report.Status, report.Kind, mainline, report.FinishedAt,
	).Scan(&workspaceID)
	if err != nil {
		return result, err
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO runs (workspace_id, kind, status, branch, commit_sha, pull_request, run_url, bogform_version,
			counts, changes, outputs_changed, error, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		RETURNING id`,
		workspaceID, report.Kind, report.Status, report.Run.Branch, report.Run.Commit, report.Run.PullRequest, report.Run.RunURL, report.BogformVersion,
		counts, changes, outputs, report.Error, report.StartedAt, report.FinishedAt,
	).Scan(&result.runID)
	if err != nil {
		return result, err
	}

	// The conditions compare with the time of the latest mainline run, which is kept in runs, so that a
	// report arriving late doesn't replace newer status.
	_, err = tx.ExecContext(ctx, `
		UPDATE workspaces w SET last_status = $2, last_kind = $3
		WHERE w.id = $1 AND ($4 OR NOT w.status_from_mainline) AND NOT EXISTS (
			SELECT 1 FROM runs r WHERE r.workspace_id = w.id AND r.id <> $5 AND r.pull_request = '' AND r.finished_at > $6
		)`, workspaceID, report.Status, report.Kind, mainline, result.runID, report.FinishedAt)
	if err != nil {
		return result, err
	}

	// Drift status comes only from drift checks, and an apply clears it, because it should have brought
	// infrastructure and configuration back in line.
	if mainline {
		switch {
		case report.Kind == ci.KindDrift && report.Status != ci.StatusFailed:
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET drift_status = $2, drift_checked_at = $3 WHERE id = $1`,
				workspaceID, report.Status, report.FinishedAt)
		case report.Kind == ci.KindApply && report.Status != ci.StatusFailed:
			_, err = tx.ExecContext(ctx, `UPDATE workspaces SET drift_status = '', last_apply_at = $2 WHERE id = $1`,
				workspaceID, report.FinishedAt)
		}
		if err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

// Prune deletes runs past the retention period, so that low-cost databases don't fill up.
func Prune(ctx context.Context, db *sql.DB, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	result, err := db.ExecContext(ctx, `DELETE FROM runs WHERE finished_at < now() - make_interval(days => $1)`, retentionDays)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

const runColumns = `r.id, r.workspace_id, w.repository, w.directory, w.name, r.kind, r.status, r.branch, r.commit_sha,
	r.pull_request, r.run_url, r.bogform_version, r.counts, r.outputs_changed, r.error, r.started_at, r.finished_at`

func scanRun(row interface{ Scan(...any) error }, extra ...any) (Run, error) {
	var run Run
	var counts, outputs []byte
	dest := append([]any{&run.ID, &run.WorkspaceID, &run.Repository, &run.Directory, &run.WorkspaceName, &run.Kind, &run.Status, &run.Branch, &run.Commit,
		&run.PullRequest, &run.RunURL, &run.BogformVersion, &counts, &outputs, &run.Error, &run.StartedAt, &run.FinishedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return run, err
	}
	if err := json.Unmarshal(counts, &run.Counts); err != nil {
		return run, err
	}
	if len(outputs) > 0 {
		if err := json.Unmarshal(outputs, &run.OutputsChanged); err != nil {
			return run, err
		}
	}
	return run, nil
}

// AuditEntry records something a person did.
func recordAudit(ctx context.Context, db *sql.DB, actor, action, target string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4)`, actor, action, target, raw)
	return err
}
