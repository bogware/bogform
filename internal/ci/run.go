// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// WorkDir is the directory, relative to the configuration, where runs keep their plan and report.
const WorkDir = ".bogform-ci"

// Exit codes of the "ci" commands.
const (
	ExitOK      = 0
	ExitFailed  = 1
	ExitDrifted = 2
)

// Options configures a run.
type Options struct {
	Binary    string // The bogform executable to run.
	Version   string
	Directory string // The configuration directory, for display.
	Workspace string
	Vars      []string
	VarFiles  []string
	Args      []string // More arguments for the plan command.

	AllowPullRequestApply bool
	NoComment             bool
	NoOutput              bool

	Env        []string // Environment of this process, as os.Environ returns it.
	Stdout     io.Writer
	Stderr     io.Writer
	HTTPClient *http.Client
	// Publisher, if set, receives the report when a run finishes.
	Publisher func(context.Context, *Report) error
	Now       func() time.Time
}

// privateEnvNames are only for this process, so they're withheld from what it runs. BOG_ variables are
// also copied to TF_ at startup, so both forms are listed.
var privateEnvNames = []string{TokenEnvName, "TF_CI_TOKEN", ServerTokenEnvName, "TF_SERVER_TOKEN"}

// Runner carries out "ci" commands by running the bogform executable, exactly as a person would.
type Runner struct {
	Options
	Context Context
	token   string
	env     []string
}

// NewRunner detects the pipeline and prepares the environment for the commands it runs.
func NewRunner(opts Options) *Runner {
	lookup := func(name string) string {
		prefix := name + "="
		for _, entry := range opts.Env {
			if strings.HasPrefix(entry, prefix) {
				return entry[len(prefix):]
			}
		}
		return ""
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Workspace == "" {
		opts.Workspace = lookup("BOG_WORKSPACE")
	}
	if opts.Workspace == "" {
		opts.Workspace = lookup("TF_WORKSPACE")
	}

	env := make([]string, 0, len(opts.Env)+2)
	for _, entry := range opts.Env {
		if slices.ContainsFunc(privateEnvNames, func(name string) bool { return strings.HasPrefix(entry, name+"=") }) {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "TF_IN_AUTOMATION=1", "TF_INPUT=0")

	runner := &Runner{Options: opts, Context: Detect(lookup), token: lookup(TokenEnvName), env: env}
	if serverURL := lookup(ServerURLEnvName); serverURL != "" && runner.Publisher == nil {
		publisher, err := NewPublisher(serverURL, lookup(ServerTokenEnvName), opts.HTTPClient)
		if err != nil {
			runner.warn("not sending reports to the Bogform server: %s", err)
		} else {
			runner.Publisher = publisher
		}
	}
	return runner
}

// command runs bogform with the given arguments and returns what it wrote to stdout. When stream is
// true the output is also shown live, because pipeline logs are the place people look first.
func (r *Runner) command(ctx context.Context, stream bool, args ...string) (stdout []byte, combined string, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Env = r.env
	var out, all bytes.Buffer
	if stream {
		cmd.Stdout = io.MultiWriter(&out, &all, r.Stdout)
		cmd.Stderr = io.MultiWriter(&all, r.Stderr)
	} else {
		cmd.Stdout = &out
		cmd.Stderr = &all
	}
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), all.String(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, "", -1, err
	}
	return out.Bytes(), all.String(), 0, nil
}

func (r *Runner) newReport(kind string) *Report {
	return &Report{
		FormatVersion:  ReportFormatVersion,
		Kind:           kind,
		BogformVersion: r.Version,
		Directory:      r.Directory,
		Workspace:      cmpOr(r.Workspace, "default"),
		Run:            r.Context,
		Changes:        []Change{},
		StartedAt:      r.Now().UTC(),
	}
}

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// prepare initializes the working directory and selects the workspace.
func (r *Runner) prepare(ctx context.Context, report *Report) bool {
	if err := os.MkdirAll(WorkDir, 0o755); err != nil {
		return r.fail(report, fmt.Sprintf("Couldn't create %s: %s", WorkDir, err), "")
	}
	// Plans and reports must never be committed: a saved plan can hold sensitive values.
	if err := os.WriteFile(filepath.Join(WorkDir, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return r.fail(report, fmt.Sprintf("Couldn't write to %s: %s", WorkDir, err), "")
	}
	_, output, code, err := r.command(ctx, true, "init", "-input=false", "-no-color")
	if err != nil {
		return r.fail(report, fmt.Sprintf("Couldn't run bogform: %s", err), "")
	}
	if code != 0 {
		return r.fail(report, "`bogform init` failed.", output)
	}
	if r.Workspace != "" && r.Workspace != "default" {
		_, output, code, err = r.command(ctx, true, "workspace", "select", "-or-create", "-no-color", r.Workspace)
		if err != nil {
			return r.fail(report, fmt.Sprintf("Couldn't run bogform: %s", err), "")
		}
		if code != 0 {
			return r.fail(report, "Selecting the workspace failed.", output)
		}
	}
	return true
}

func (r *Runner) fail(report *Report, message, output string) bool {
	report.Status = StatusFailed
	report.Error = message
	report.Output = output
	return false
}

// plan runs a plan and summarizes it in the report. It returns the path of the saved plan, whether the
// plan has changes, and whether it succeeded.
func (r *Runner) plan(ctx context.Context, report *Report, extra ...string) (string, bool, bool) {
	planPath := filepath.Join(WorkDir, "plan.tfplan")
	args := []string{"plan", "-input=false", "-no-color", "-detailed-exitcode", "-out=" + planPath}
	args = append(args, extra...)
	for _, file := range r.VarFiles {
		args = append(args, "-var-file="+file)
	}
	for _, variable := range r.Vars {
		args = append(args, "-var="+variable)
	}
	args = append(args, r.Args...)

	_, output, code, err := r.command(ctx, true, args...)
	if err != nil {
		return "", false, r.fail(report, fmt.Sprintf("Couldn't run bogform: %s", err), "")
	}
	report.Output = output
	if code != 0 && code != 2 {
		return "", false, r.fail(report, "`bogform plan` failed.", output)
	}
	raw, showOutput, showCode, err := r.command(ctx, false, "show", "-json", "-no-color", planPath)
	if err != nil {
		return "", false, r.fail(report, fmt.Sprintf("Couldn't run bogform: %s", err), "")
	}
	if showCode != 0 {
		return "", false, r.fail(report, "Reading the plan failed.", showOutput)
	}
	if err := report.summarizePlan(raw); err != nil {
		return "", false, r.fail(report, err.Error(), "")
	}
	return planPath, code == 2, true
}

// Plan runs "ci plan".
func (r *Runner) Plan(ctx context.Context) int {
	report := r.newReport(KindPlan)
	if r.prepare(ctx, report) {
		if _, changed, ok := r.plan(ctx, report); ok {
			report.Status = StatusNoChanges
			if changed {
				report.Status = StatusChanges
			}
		}
	}
	return r.finish(ctx, report)
}

// Apply runs "ci apply".
func (r *Runner) Apply(ctx context.Context) int {
	report := r.newReport(KindApply)
	switch {
	case r.Context.IsPullRequest && !r.AllowPullRequestApply:
		// Code on a pull request hasn't been reviewed. Applying it could run anything with the
		// pipeline's cloud credentials.
		r.fail(report, "Refusing to apply from a pull request, because its code might not be reviewed. Run this on your default branch after merging, or use -allow-pull-request-apply if you understand the risk.", "")
	case r.prepare(ctx, report):
		planPath, changed, ok := r.plan(ctx, report)
		if !ok {
			break
		}
		if !changed {
			report.Status = StatusNoChanges
			break
		}
		_, output, code, err := r.command(ctx, true, "apply", "-input=false", "-no-color", planPath)
		switch {
		case err != nil:
			r.fail(report, fmt.Sprintf("Couldn't run bogform: %s", err), "")
		case code != 0:
			r.fail(report, "`bogform apply` failed. Some changes might have been made.", output)
		default:
			report.Status = StatusApplied
			report.Output = output
		}
	}
	return r.finish(ctx, report)
}

// Drift runs "ci drift".
func (r *Runner) Drift(ctx context.Context) int {
	report := r.newReport(KindDrift)
	if r.prepare(ctx, report) {
		if _, changed, ok := r.plan(ctx, report, "-refresh-only"); ok {
			report.Status = StatusNoChanges
			if changed {
				report.Status = StatusDrifted
			}
		}
	}
	code := r.finish(ctx, report)
	if code == ExitOK && report.Status == StatusDrifted {
		return ExitDrifted
	}
	return code
}

// finish publishes the report everywhere it's configured to go. Problems here are warnings: they
// shouldn't fail a run whose real work succeeded.
func (r *Runner) finish(ctx context.Context, report *Report) int {
	report.FinishedAt = r.Now().UTC()

	if raw, err := json.MarshalIndent(report, "", "  "); err != nil {
		r.warn("couldn't encode the report: %s", err)
	} else if err := os.MkdirAll(WorkDir, 0o755); err != nil {
		r.warn("couldn't write the report: %s", err)
	} else if err := os.WriteFile(filepath.Join(WorkDir, "report.json"), raw, 0o644); err != nil {
		r.warn("couldn't write the report: %s", err)
	}

	r.writeSummary(report)
	r.postComment(ctx, report)
	if r.Publisher != nil {
		if err := r.Publisher(ctx, report); err != nil {
			r.warn("couldn't send the report to the Bogform server: %s", err)
		}
	}

	fmt.Fprintf(r.Stdout, "\nBogform %s: %s (%d to add, %d to change, %d to replace, %d to destroy)\n",
		report.Kind, report.Status, report.Counts.Add, report.Counts.Change, report.Counts.Replace, report.Counts.Destroy)
	if report.Status == StatusFailed {
		if report.Error != "" {
			fmt.Fprintln(r.Stderr, report.Error)
		}
		return ExitFailed
	}
	return ExitOK
}

func (r *Runner) warn(format string, args ...any) {
	fmt.Fprintf(r.Stderr, "Warning: "+format+"\n", args...)
}

func (r *Runner) writeSummary(report *Report) {
	if r.Context.summaryPath == "" {
		return
	}
	file, err := os.OpenFile(r.Context.summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		r.warn("couldn't write the job summary: %s", err)
		return
	}
	defer file.Close()
	// GitHub limits each step's summary to 1 MiB.
	body := report.Render(RenderOptions{MaxBytes: 900000, Collapsible: true, HideOutput: r.NoOutput})
	if _, err := io.WriteString(file, body+"\n"); err != nil {
		r.warn("couldn't write the job summary: %s", err)
	}
}

func (r *Runner) postComment(ctx context.Context, report *Report) {
	// Apply and drift runs happen outside pull requests, and comments only make sense on plans
	// unless someone has explicitly applied from a pull request.
	if r.NoComment || !r.Context.IsPullRequest {
		return
	}
	commenter, err := NewCommenter(r.Context, r.token, r.HTTPClient)
	if err != nil {
		if !errors.Is(err, ErrNoCommenter) {
			r.warn("not commenting on the pull request: %s", err)
		}
		return
	}
	body := report.Render(RenderOptions{MaxBytes: commenter.MaxBody(), Collapsible: commenter.Collapsible(), HideOutput: r.NoOutput})
	marker := Marker(report.Kind, report.Directory, report.Workspace)
	if err := commenter.Upsert(ctx, marker, body); err != nil {
		if r.Context.FromFork {
			r.warn("couldn't comment, which is expected for a pull request from a fork: %s", err)
			return
		}
		r.warn("couldn't comment on the pull request: %s", err)
	}
}
