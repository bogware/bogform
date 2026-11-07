// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/ci/cost"
)

// Kinds of run.
const (
	KindPlan  = "plan"
	KindApply = "apply"
	KindDrift = "drift"
)

// Results of a run.
const (
	StatusNoChanges = "no_changes"
	StatusChanges   = "changes"
	StatusApplied   = "applied"
	StatusDrifted   = "drifted"
	StatusFailed    = "failed"
)

// ReportFormatVersion is the version of the report.json format.
const ReportFormatVersion = 1

// Change is one resource that a plan would change. It holds no attribute values, so reports are
// safe to store and display even for resources with sensitive data.
type Change struct {
	Address string   `json:"address"`
	Actions []string `json:"actions"`
}

// Counts summarizes the changes in a plan.
type Counts struct {
	Add     int `json:"add"`
	Change  int `json:"change"`
	Destroy int `json:"destroy"`
	Replace int `json:"replace"`
	Import  int `json:"import"`
	Read    int `json:"read,omitempty"`
}

// Destructive is the number of resources that would be destroyed or replaced.
func (c Counts) Destructive() int { return c.Destroy + c.Replace }

// Total is the number of resources that would change.
func (c Counts) Total() int { return c.Add + c.Change + c.Destroy + c.Replace + c.Import }

// Report is the record of a pipeline run, written to report.json.
type Report struct {
	FormatVersion  int      `json:"format_version"`
	Kind           string   `json:"kind"`
	Status         string   `json:"status"`
	BogformVersion string   `json:"bogform_version"`
	Directory      string   `json:"directory"`
	Workspace      string   `json:"workspace"`
	Run            Context  `json:"run"`
	Counts         Counts   `json:"counts"`
	Changes        []Change `json:"changes"`
	OutputsChanged []string `json:"outputs_changed,omitempty"`
	// Checks are the built-in checks that planned resources don't pass. They're warnings.
	Checks []Finding `json:"checks,omitempty"`
	// Cost is the monthly cost estimate, when one was asked for.
	Cost *cost.Estimate `json:"cost,omitempty"`
	// DestroyAllowedBy says what allowed an apply to destroy or replace resources.
	DestroyAllowedBy string    `json:"destroy_allowed_by,omitempty"`
	Error            string    `json:"error,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	FinishedAt       time.Time `json:"finished_at"`

	// Output is the text of the plan or the failure. Bogform redacts sensitive values in it, but a
	// provider could still print something private, so it's left out of report.json.
	Output string `json:"-"`
}

// planJSON is the part of the "show -json" format that reports use. Attribute values are deliberately
// not decoded, because they can include sensitive data.
type planJSON struct {
	ResourceChanges []struct {
		Address string `json:"address"`
		Mode    string `json:"mode"`
		Change  struct {
			Actions   []string `json:"actions"`
			Importing *struct {
				ID string `json:"id"`
			} `json:"importing"`
		} `json:"change"`
	} `json:"resource_changes"`
	OutputChanges map[string]struct {
		Actions []string `json:"actions"`
	} `json:"output_changes"`
}

// summarizePlan fills in the report's changes from the output of "show -json".
func (r *Report) summarizePlan(raw []byte) error {
	var plan planJSON
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("couldn't read the plan: %w", err)
	}
	r.Counts = Counts{}
	r.Changes = nil
	for _, resource := range plan.ResourceChanges {
		actions := resource.Change.Actions
		importing := resource.Change.Importing != nil
		if slices.Equal(actions, []string{"no-op"}) && !importing {
			continue
		}
		switch {
		case importing && slices.Equal(actions, []string{"no-op"}):
			r.Counts.Import++
		case slices.Equal(actions, []string{"read"}):
			r.Counts.Read++
		case slices.Equal(actions, []string{"create"}):
			r.Counts.Add++
		case slices.Equal(actions, []string{"update"}):
			r.Counts.Change++
		case slices.Equal(actions, []string{"delete"}):
			r.Counts.Destroy++
		case slices.Contains(actions, "create") && slices.Contains(actions, "delete"):
			r.Counts.Replace++
		}
		r.Changes = append(r.Changes, Change{Address: resource.Address, Actions: actions})
	}
	r.OutputsChanged = nil
	for name, output := range plan.OutputChanges {
		if !slices.Equal(output.Actions, []string{"no-op"}) {
			r.OutputsChanged = append(r.OutputsChanged, name)
		}
	}
	slices.Sort(r.OutputsChanged)
	return nil
}

// Marker identifies this directory and workspace's comment on a pull request. A Markdown link
// reference doesn't render, and unlike an HTML comment that works on every platform.
func Marker(kind, directory, workspace string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + directory + "\x00" + workspace))
	return "[//]: # (bogform-ci:" + hex.EncodeToString(sum[:8]) + ")"
}

var statusHeadings = map[string]string{
	StatusNoChanges: "✅ No changes",
	StatusChanges:   "📝 Changes planned",
	StatusApplied:   "🚀 Applied",
	StatusDrifted:   "⚠️ Drift detected",
	StatusFailed:    "❌ Failed",
}

// RenderOptions controls how a report is rendered as Markdown.
type RenderOptions struct {
	// MaxBytes limits the size of the result. Zero means no limit.
	MaxBytes int
	// Collapsible puts long sections inside HTML <details> blocks.
	Collapsible bool
	// HideOutput leaves out the text of the plan.
	HideOutput bool
}

// Render formats the report as Markdown.
func (r *Report) Render(opts RenderOptions) string {
	var head strings.Builder
	head.WriteString(Marker(r.Kind, r.Directory, r.Workspace))
	head.WriteString("\n\n")
	fmt.Fprintf(&head, "### Bogform %s: %s\n\n", r.Kind, statusHeadings[r.Status])
	fmt.Fprintf(&head, "**Directory:** %s · **Workspace:** %s", inlineCode(r.Directory), inlineCode(r.Workspace))
	if r.Run.Commit != "" {
		fmt.Fprintf(&head, " · **Commit:** %s", inlineCode(shortCommit(r.Run.Commit)))
	}
	head.WriteString("\n\n")

	if r.Error != "" {
		fmt.Fprintf(&head, "%s\n\n", r.Error)
	}
	if r.Counts.Total() > 0 || r.Status == StatusChanges || r.Status == StatusApplied || r.Status == StatusDrifted {
		fmt.Fprintf(&head, "**%d** to add · **%d** to change · **%d** to replace · **%d** to destroy", r.Counts.Add, r.Counts.Change, r.Counts.Replace, r.Counts.Destroy)
		if r.Counts.Import > 0 {
			fmt.Fprintf(&head, " · **%d** to import", r.Counts.Import)
		}
		head.WriteString("\n\n")
	}
	head.WriteString(r.renderDestroys())
	head.WriteString(renderChecks(r.Checks))
	head.WriteString(renderCost(r.Cost, opts.Collapsible))
	head.WriteString(renderChanges(r.Changes))
	if len(r.OutputsChanged) > 0 {
		quoted := make([]string, len(r.OutputsChanged))
		for i, name := range r.OutputsChanged {
			quoted[i] = inlineCode(name)
		}
		fmt.Fprintf(&head, "**Outputs changed:** %s\n\n", strings.Join(quoted, ", "))
	}

	var foot strings.Builder
	if r.Run.RunURL != "" {
		fmt.Fprintf(&foot, "[View this run](%s)", r.Run.RunURL)
		foot.WriteString(" · ")
	}
	fmt.Fprintf(&foot, "Bogform %s\n", r.BogformVersion)

	output := ""
	if !opts.HideOutput && strings.TrimSpace(r.Output) != "" {
		budget := 0
		if opts.MaxBytes > 0 {
			budget = opts.MaxBytes - head.Len() - foot.Len() - 200
		}
		if budget >= 0 {
			text := truncateMiddle(strings.TrimSpace(r.Output), budget)
			fence := codeFence(text)
			block := fmt.Sprintf("%s\n%s\n%s\n", fence, text, fence)
			if opts.Collapsible {
				output = fmt.Sprintf("<details><summary>Show output</summary>\n\n%s\n</details>\n\n", block)
			} else {
				output = block + "\n"
			}
		}
	}
	return head.String() + output + foot.String()
}

// maxListedChanges limits the table's length. The full list stays in report.json.
const maxListedChanges = 100

func renderChanges(changes []Change) string {
	if len(changes) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("| Action | Resource |\n|---|---|\n")
	for i, change := range changes {
		if i == maxListedChanges {
			fmt.Fprintf(&out, "| | ...and %d more |\n", len(changes)-maxListedChanges)
			break
		}
		fmt.Fprintf(&out, "| %s | %s |\n", actionLabel(change.Actions), inlineCode(change.Address))
	}
	out.WriteString("\n")
	return out.String()
}

func actionLabel(actions []string) string {
	switch {
	case slices.Equal(actions, []string{"create"}):
		return "➕ create"
	case slices.Equal(actions, []string{"update"}):
		return "✏️ update"
	case slices.Equal(actions, []string{"delete"}):
		return "🗑️ destroy"
	case slices.Equal(actions, []string{"read"}):
		return "🔍 read"
	case slices.Equal(actions, []string{"no-op"}):
		return "📥 import"
	case slices.Contains(actions, "create") && slices.Contains(actions, "delete"):
		return "♻️ replace"
	}
	return strings.Join(actions, ", ")
}

// inlineCode formats text as inline code that's safe in a table. Addresses can include user-controlled
// instance keys, so this stops them from breaking out of the code span.
func inlineCode(text string) string {
	text = strings.NewReplacer("\r", " ", "\n", " ", "|", "\\|").Replace(text)
	longest, current := 0, 0
	for _, r := range text {
		if r == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	if longest == 0 {
		return "`" + text + "`"
	}
	delimiter := strings.Repeat("`", longest+1)
	return delimiter + " " + text + " " + delimiter
}

// codeFence returns a fence longer than any run of backticks in text, so that the text can't end the
// code block and inject Markdown into the comment.
func codeFence(text string) string {
	longest, current := 2, 0
	for _, r := range text {
		if r == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", longest+1)
}

// truncateMiddle shortens text by removing its middle, because the start of a plan lists the changes
// and the end has the summary and any errors. A limit of zero or less means no limit.
func truncateMiddle(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	const notice = "\n\n... output truncated; see the full log for this run ...\n\n"
	keep := limit - len(notice)
	if keep < 100 {
		return strings.ToValidUTF8(text[:min(limit, len(text))], "")
	}
	headLen := keep * 2 / 3
	tailLen := keep - headLen
	return strings.ToValidUTF8(text[:headLen], "") + notice + strings.ToValidUTF8(text[len(text)-tailLen:], "")
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	return commit
}

// maxListedFindings limits how many destroys, check warnings and priced resources are listed in a
// comment. Everything stays in report.json.
const maxListedFindings = 50

// destroyedAddresses lists the resources that a plan destroys or replaces.
func (r *Report) destroyedAddresses() []Change {
	var result []Change
	for _, change := range r.Changes {
		if slices.Contains(change.Actions, "delete") {
			result = append(result, change)
		}
	}
	return result
}

// allowDestroyHint explains how to allow an apply that destroys resources on each platform.
func allowDestroyHint(platform string) string {
	switch platform {
	case PlatformGitHub:
		return "add the `allow-destroy` label to the pull request before it merges, or tick **allow destroy** when you run the workflow"
	case PlatformGitLab:
		return "run the apply job with the `BOG_CI_ALLOW_DESTROY` variable set to `true`"
	case PlatformAzure:
		return "run the pipeline with **Allow destroy** checked"
	case PlatformBitbucket:
		return "run the apply pipeline with `BOG_CI_ALLOW_DESTROY` set to `true`"
	}
	return "run `bogform ci apply -allow-destroy`"
}

func (r *Report) renderDestroys() string {
	destroyed := r.destroyedAddresses()
	if len(destroyed) == 0 {
		return ""
	}
	var out strings.Builder
	noun := "resources"
	if len(destroyed) == 1 {
		noun = "resource"
	}
	verb := "This plan destroys or replaces"
	if r.Kind == KindApply && r.Status == StatusApplied {
		verb = "This apply destroyed or replaced"
	}
	fmt.Fprintf(&out, "#### ⚠️ %s %d %s\n\n", verb, len(destroyed), noun)
	for i, change := range destroyed {
		if i == maxListedFindings {
			fmt.Fprintf(&out, "- ...and %d more\n", len(destroyed)-maxListedFindings)
			break
		}
		fmt.Fprintf(&out, "- %s %s\n", actionLabel(change.Actions), inlineCode(change.Address))
	}
	out.WriteString("\n")
	switch {
	case r.DestroyAllowedBy != "":
		fmt.Fprintf(&out, "Allowed by %s.\n\n", r.DestroyAllowedBy)
	case r.Kind == KindPlan:
		fmt.Fprintf(&out, "Applying this needs approval: %s.\n\n", allowDestroyHint(r.Run.Platform))
	}
	return out.String()
}

// tableText makes text safe in a Markdown table cell.
func tableText(text string) string {
	return strings.NewReplacer("|", "\\|", "\r", " ", "\n", " ").Replace(text)
}

func renderChecks(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var out strings.Builder
	noun := "warnings"
	if len(findings) == 1 {
		noun = "warning"
	}
	fmt.Fprintf(&out, "#### 🛡️ %d check %s\n\n| Check | Resource | Problem |\n|---|---|---|\n", len(findings), noun)
	for i, finding := range findings {
		if i == maxListedFindings {
			fmt.Fprintf(&out, "| | | ...and %d more |\n", len(findings)-maxListedFindings)
			break
		}
		fmt.Fprintf(&out, "| %s | %s | %s |\n", inlineCode(finding.Check), inlineCode(finding.Address), tableText(finding.Message))
	}
	fmt.Fprintf(&out, "\nTo skip a check for one resource, give it a %s tag with the check's ID, or `all`.\n\n", inlineCode(SkipChecksTag))
	return out.String()
}

func renderCost(estimate *cost.Estimate, collapsible bool) string {
	if estimate == nil {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "#### 💰 Estimated monthly cost: %s → %s (%s)\n\n",
		cost.FormatMoney(estimate.Before), cost.FormatMoney(estimate.After), cost.FormatDelta(estimate.Delta()))
	if len(estimate.Resources) > 0 {
		out.WriteString("| Resource | Monthly | Change |\n|---|---|---|\n")
		for i, line := range estimate.Resources {
			if i == maxListedFindings {
				fmt.Fprintf(&out, "| ...and %d more | | |\n", len(estimate.Resources)-maxListedFindings)
				break
			}
			fmt.Fprintf(&out, "| %s<br>%s | %s | %s |\n", inlineCode(line.Address), tableText(line.Description), cost.FormatMoney(line.After), cost.FormatDelta(line.After-line.Before))
		}
		out.WriteString("\n")
	}
	for _, warning := range estimate.Warnings {
		fmt.Fprintf(&out, "Couldn't price everything: %s\n\n", strings.ReplaceAll(warning, "\n", " "))
	}
	if len(estimate.NotPriced) > 0 {
		var list strings.Builder
		for i, item := range estimate.NotPriced {
			if i == maxListedFindings {
				fmt.Fprintf(&list, "- ...and %d more\n", len(estimate.NotPriced)-maxListedFindings)
				break
			}
			fmt.Fprintf(&list, "- %s: %s\n", inlineCode(item.Address), item.Reason)
		}
		summary := fmt.Sprintf("%d changed resources aren't in the estimate", len(estimate.NotPriced))
		if len(estimate.NotPriced) == 1 {
			summary = "1 changed resource isn't in the estimate"
		}
		if collapsible {
			fmt.Fprintf(&out, "<details><summary>%s</summary>\n\n%s\n</details>\n\n", summary, list.String())
		} else {
			fmt.Fprintf(&out, "%s:\n\n%s\n", summary, list.String())
		}
	}
	out.WriteString("On-demand list prices in US dollars, before tax, discounts, and usage-based charges.\n\n")
	return out.String()
}
