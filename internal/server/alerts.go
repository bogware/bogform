// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

// Events that can send an alert.
const (
	EventApplySucceeded = "apply_succeeded"
	EventApplyFailed    = "apply_failed"
	EventPlanFailed     = "plan_failed"
	EventDriftDetected  = "drift_detected"
	EventDriftResolved  = "drift_resolved"
)

// AllEvents lists every event, in the order the documentation describes them.
var AllEvents = []string{EventApplySucceeded, EventApplyFailed, EventPlanFailed, EventDriftDetected, EventDriftResolved}

// AlertConfig says where alerts go. Every destination is optional.
type AlertConfig struct {
	SlackWebhook  string
	TeamsWebhook  string
	EmailFrom     string
	EmailTo       []string
	WebhookURL    string
	WebhookSecret string
	// Events and Repositories limit which runs send alerts. Empty means all. Repositories are patterns
	// for path.Match, such as "acme/*".
	Events       []string
	Repositories []string
}

func (c AlertConfig) enabled() bool {
	return c.SlackWebhook != "" || c.TeamsWebhook != "" || c.EmailFrom != "" || c.WebhookURL != ""
}

func (c AlertConfig) wants(event, repository string) bool {
	if len(c.Events) > 0 && !slices.Contains(c.Events, event) {
		return false
	}
	if len(c.Repositories) == 0 {
		return true
	}
	for _, pattern := range c.Repositories {
		if matched, err := path.Match(pattern, repository); err == nil && matched {
			return true
		}
	}
	return false
}

func (c AlertConfig) validate(add func(string, ...any)) {
	for _, event := range c.Events {
		if !slices.Contains(AllEvents, event) {
			add("BOG_SERVER_ALERT_EVENTS: %q isn't an event. Use any of %s", event, strings.Join(AllEvents, ", "))
		}
	}
	for _, pattern := range c.Repositories {
		if _, err := path.Match(pattern, "x"); err != nil {
			add("BOG_SERVER_ALERT_REPOSITORIES: %q isn't a valid pattern", pattern)
		}
	}
	for name, value := range map[string]string{"SLACK_WEBHOOK": c.SlackWebhook, "TEAMS_WEBHOOK": c.TeamsWebhook, "WEBHOOK_URL": c.WebhookURL} {
		if value == PlaceholderSecret {
			add("BOG_SERVER_ALERT_%s still has its placeholder value", name)
		} else if value != "" && !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
			add("BOG_SERVER_ALERT_%s must be an http or https address", name)
		}
	}
	if (c.EmailFrom == "") != (len(c.EmailTo) == 0) {
		add("BOG_SERVER_ALERT_EMAIL_FROM and BOG_SERVER_ALERT_EMAIL_TO must be set together")
	}
	if c.WebhookURL != "" && (c.WebhookSecret == "" || c.WebhookSecret == PlaceholderSecret) {
		add("BOG_SERVER_ALERT_WEBHOOK_SECRET is required with BOG_SERVER_ALERT_WEBHOOK_URL, so that receivers can check where alerts came from")
	}
}

// Alert describes one event. It goes to webhooks as JSON, so its fields are part of a public format.
type Alert struct {
	Event        string    `json:"event"`
	Summary      string    `json:"summary"`
	Platform     string    `json:"platform"`
	Repository   string    `json:"repository"`
	Directory    string    `json:"directory"`
	Workspace    string    `json:"workspace"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Counts       ci.Counts `json:"counts"`
	Branch       string    `json:"branch,omitempty"`
	Commit       string    `json:"commit,omitempty"`
	PullRequest  string    `json:"pull_request,omitempty"`
	Actor        string    `json:"actor,omitempty"`
	Trigger      string    `json:"trigger,omitempty"`
	Error        string    `json:"error,omitempty"`
	DashboardURL string    `json:"dashboard_url,omitempty"`
	PipelineURL  string    `json:"pipeline_url,omitempty"`
	FinishedAt   time.Time `json:"finished_at"`
}

// alertEvent decides which event a report is, if any. Drift alerts only come when drift starts or ends,
// so that a drift check on a schedule doesn't repeat the same alert.
func alertEvent(report *ci.Report, previousDrift string) string {
	mainline := !report.Run.IsPullRequest
	switch {
	case report.Kind == ci.KindApply && report.Status == ci.StatusApplied:
		return EventApplySucceeded
	case report.Kind == ci.KindApply && report.Status == ci.StatusFailed:
		return EventApplyFailed
	case report.Kind == ci.KindPlan && report.Status == ci.StatusFailed:
		return EventPlanFailed
	case report.Kind == ci.KindDrift && mainline && report.Status == ci.StatusDrifted && previousDrift != ci.StatusDrifted:
		return EventDriftDetected
	case report.Kind == ci.KindDrift && mainline && report.Status == ci.StatusNoChanges && previousDrift == ci.StatusDrifted:
		return EventDriftResolved
	}
	return ""
}

const maxAlertError = 500

func newAlert(event string, report *ci.Report, dashboardURL string) Alert {
	alert := Alert{
		Event:        event,
		Platform:     report.Run.Platform,
		Repository:   report.Run.Repository,
		Directory:    report.Directory,
		Workspace:    report.Workspace,
		Kind:         report.Kind,
		Status:       report.Status,
		Counts:       report.Counts,
		Branch:       report.Run.Branch,
		Commit:       report.Run.Commit,
		PullRequest:  report.Run.PullRequest,
		Actor:        report.Run.Actor,
		Trigger:      report.Run.Trigger,
		Error:        report.Error,
		DashboardURL: dashboardURL,
		PipelineURL:  report.Run.RunURL,
		FinishedAt:   report.FinishedAt,
	}
	if len(alert.Error) > maxAlertError {
		alert.Error = strings.ToValidUTF8(alert.Error[:maxAlertError], "") + "…"
	}
	alert.Summary = fmt.Sprintf("%s: %s", alertTitle(event), alert.target())
	return alert
}

func alertTitle(event string) string {
	switch event {
	case EventApplySucceeded:
		return "Applied"
	case EventApplyFailed:
		return "Apply failed"
	case EventPlanFailed:
		return "Plan failed"
	case EventDriftDetected:
		return "Drift detected"
	case EventDriftResolved:
		return "Drift resolved"
	}
	return event
}

// target names the configuration, such as "acme/infra (network, prod)".
func (a Alert) target() string {
	return fmt.Sprintf("%s (%s, %s)", a.Repository, a.Directory, a.Workspace)
}

// changes describes the counts, such as "2 to add, 1 to destroy", or "" if there are none.
func (a Alert) changes() string {
	verb := map[bool]string{true: "added", false: "to add"}[a.Event == EventApplySucceeded]
	var parts []string
	add := func(count int, label string) {
		if count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, label))
		}
	}
	add(a.Counts.Add, verb)
	if a.Event == EventApplySucceeded {
		add(a.Counts.Change, "changed")
		add(a.Counts.Replace, "replaced")
		add(a.Counts.Destroy, "destroyed")
		add(a.Counts.Import, "imported")
	} else {
		add(a.Counts.Change, "to change")
		add(a.Counts.Replace, "to replace")
		add(a.Counts.Destroy, "to destroy")
		add(a.Counts.Import, "to import")
	}
	return strings.Join(parts, ", ")
}

// source describes where the run came from, such as "pull request 12 by octocat".
func (a Alert) source() string {
	var where string
	switch {
	case a.PullRequest != "":
		where = "pull request " + a.PullRequest
	case a.Branch != "":
		where = a.Branch
	}
	if a.Commit != "" {
		short := a.Commit
		if len(short) > 8 {
			short = short[:8]
		}
		if where != "" {
			where += " at " + short
		} else {
			where = short
		}
	}
	if a.Actor != "" {
		where = strings.TrimSpace(where + " by " + a.Actor)
	}
	if a.Trigger != "" {
		where = strings.TrimSpace(where + " (" + a.Trigger + ")")
	}
	return where
}

// facts are the details shown in every format, in order.
func (a Alert) facts() [][2]string {
	facts := [][2]string{{"Repository", a.Repository}, {"Directory", a.Directory}, {"Workspace", a.Workspace}}
	if changes := a.changes(); changes != "" {
		facts = append(facts, [2]string{"Changes", changes})
	}
	if source := a.source(); source != "" {
		facts = append(facts, [2]string{"From", source})
	}
	return facts
}

// emailSender sends a plain text email. The server uses SES, and tests use a fake.
type emailSender interface {
	Send(ctx context.Context, from string, to []string, subject, body string) error
}

// alerter sends alerts in the background. A slow or broken destination must never delay or fail a
// report, so each send has a short timeout and one retry, and errors are only logged.
type alerter struct {
	cfg    AlertConfig
	client *http.Client
	email  emailSender
	logf   func(string, ...any)
	now    func() time.Time
	retry  time.Duration
	wg     sync.WaitGroup
}

const alertTimeout = 5 * time.Second

func newAlerter(cfg AlertConfig, email emailSender, logf func(string, ...any)) *alerter {
	return &alerter{
		cfg:    cfg,
		client: &http.Client{Timeout: alertTimeout},
		email:  email,
		logf:   logf,
		now:    time.Now,
		retry:  time.Second,
	}
}

// notify sends the alert to every destination, without waiting.
func (a *alerter) notify(alert Alert) {
	if a == nil || !a.cfg.enabled() || !a.cfg.wants(alert.Event, alert.Repository) {
		return
	}
	send := func(name string, fn func(context.Context) error) {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			var err error
			for attempt := range 2 {
				if attempt > 0 {
					time.Sleep(a.retry)
				}
				ctx, cancel := context.WithTimeout(context.Background(), alertTimeout)
				err = fn(ctx)
				cancel()
				if err == nil || !retryable(err) {
					break
				}
			}
			if err != nil {
				a.logf("sending a %s alert to %s for %s: %s", alert.Event, name, alert.Repository, err)
			}
		}()
	}
	if a.cfg.SlackWebhook != "" {
		send("Slack", func(ctx context.Context) error { return a.post(ctx, a.cfg.SlackWebhook, slackMessage(alert), nil) })
	}
	if a.cfg.TeamsWebhook != "" {
		send("Teams", func(ctx context.Context) error { return a.post(ctx, a.cfg.TeamsWebhook, teamsMessage(alert), nil) })
	}
	if a.cfg.WebhookURL != "" {
		send("the webhook", func(ctx context.Context) error { return a.postSigned(ctx, alert) })
	}
	if a.cfg.EmailFrom != "" && a.email != nil {
		subject, body := emailMessage(alert)
		send("email", func(ctx context.Context) error {
			return a.email.Send(ctx, a.cfg.EmailFrom, a.cfg.EmailTo, subject, body)
		})
	}
}

// wait blocks until every alert has been sent or has failed. Lambda freezes a function between
// requests, so it calls this after each response.
func (a *alerter) wait() {
	if a != nil {
		a.wg.Wait()
	}
}

// statusError is a destination's error response.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("the destination answered %d", e.code) }

// retryable is true for errors that might not happen again: network problems, rate limits, and the
// destination's own failures. Requests that it rejected won't succeed when repeated.
func retryable(err error) bool {
	var status *statusError
	if errors.As(err, &status) {
		return status.code == http.StatusTooManyRequests || status.code >= 500
	}
	return true
}

func (a *alerter) post(ctx context.Context, url string, payload any, headers map[string]string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return a.send(ctx, url, body, headers)
}

func (a *alerter) send(ctx context.Context, url string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		// The error would include the URL, which is a secret for Slack and Teams.
		return errors.New("the address isn't valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bogform-server")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res, err := a.client.Do(req)
	if err != nil {
		// Leave out the URL here too. A timeout or a refused connection is enough to act on.
		var urlErr interface{ Timeout() bool }
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return errors.New("the request timed out")
		}
		return errors.New("the request failed: " + unwrapAll(err).Error())
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	if res.StatusCode >= 300 {
		return &statusError{code: res.StatusCode}
	}
	return nil
}

func unwrapAll(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

// Webhook signatures. Receivers compute HMAC-SHA256 of the timestamp, a period and the raw body, with the
// shared secret, and compare it with the signature header. Checking the timestamp stops replays.
const (
	headerSignature = "X-Bogform-Signature"
	headerTimestamp = "X-Bogform-Timestamp"
	headerEvent     = "X-Bogform-Event"
)

// SignWebhook returns the signature header's value for a body sent at timestamp.
func SignWebhook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhook checks a signature, and that it was made within tolerance of now.
func VerifyWebhook(secret, signature, timestamp string, body []byte, now time.Time, tolerance time.Duration) bool {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if age := now.Sub(time.Unix(seconds, 0)); age > tolerance || age < -tolerance {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(SignWebhook(secret, seconds, body)))
}

func (a *alerter) postSigned(ctx context.Context, alert Alert) error {
	body, err := json.Marshal(alert)
	if err != nil {
		return err
	}
	timestamp := a.now().Unix()
	return a.send(ctx, a.cfg.WebhookURL, body, map[string]string{
		headerEvent:     alert.Event,
		headerTimestamp: strconv.FormatInt(timestamp, 10),
		headerSignature: SignWebhook(a.cfg.WebhookSecret, timestamp, body),
	})
}

// slackEscape escapes the characters that Slack's mrkdwn treats as markup.
func slackEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func slackLink(url, text string) string {
	return "<" + strings.NewReplacer("|", "%7C", ">", "%3E", "<", "%3C").Replace(url) + "|" + slackEscape(text) + ">"
}

func alertEmoji(event string) string {
	switch event {
	case EventApplySucceeded, EventDriftResolved:
		return ":white_check_mark:"
	case EventDriftDetected:
		return ":warning:"
	}
	return ":x:"
}

func slackMessage(alert Alert) map[string]any {
	var lines []string
	for _, fact := range alert.facts() {
		lines = append(lines, fmt.Sprintf("*%s:* %s", fact[0], slackEscape(fact[1])))
	}
	if alert.Error != "" {
		lines = append(lines, "```"+strings.ReplaceAll(slackEscape(alert.Error), "```", "'''")+"```")
	}
	var links []string
	if alert.DashboardURL != "" {
		links = append(links, slackLink(alert.DashboardURL, "Dashboard"))
	}
	if alert.PipelineURL != "" {
		links = append(links, slackLink(alert.PipelineURL, "Pipeline run"))
	}
	if len(links) > 0 {
		lines = append(lines, strings.Join(links, " · "))
	}
	return map[string]any{
		// Notifications show the text. The blocks are what's shown in the channel.
		"text": slackEscape(alert.Summary),
		"blocks": []any{
			map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn",
				"text": fmt.Sprintf("%s *%s*\n%s", alertEmoji(alert.Event), slackEscape(alert.Summary), strings.Join(lines, "\n"))}},
		},
	}
}

// teamsMessage builds an Adaptive Card, which is what a Teams Workflows webhook posts.
func teamsMessage(alert Alert) map[string]any {
	color := "Attention"
	switch alert.Event {
	case EventApplySucceeded, EventDriftResolved:
		color = "Good"
	case EventDriftDetected:
		color = "Warning"
	}
	var facts []any
	for _, fact := range alert.facts() {
		facts = append(facts, map[string]string{"title": fact[0], "value": fact[1]})
	}
	body := []any{
		map[string]any{"type": "TextBlock", "text": alert.Summary, "weight": "Bolder", "size": "Medium", "color": color, "wrap": true},
		map[string]any{"type": "FactSet", "facts": facts},
	}
	if alert.Error != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": alert.Error, "fontType": "Monospace", "wrap": true, "isSubtle": true})
	}
	var actions []any
	if alert.DashboardURL != "" {
		actions = append(actions, map[string]string{"type": "Action.OpenUrl", "title": "Dashboard", "url": alert.DashboardURL})
	}
	if alert.PipelineURL != "" {
		actions = append(actions, map[string]string{"type": "Action.OpenUrl", "title": "Pipeline run", "url": alert.PipelineURL})
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	if len(actions) > 0 {
		card["actions"] = actions
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content":     card,
		}},
	}
}

func emailMessage(alert Alert) (subject, body string) {
	var b strings.Builder
	b.WriteString(alert.Summary + "\n\n")
	for _, fact := range alert.facts() {
		fmt.Fprintf(&b, "%s: %s\n", fact[0], fact[1])
	}
	if alert.Error != "" {
		fmt.Fprintf(&b, "\n%s\n", alert.Error)
	}
	b.WriteString("\n")
	if alert.DashboardURL != "" {
		fmt.Fprintf(&b, "Dashboard: %s\n", alert.DashboardURL)
	}
	if alert.PipelineURL != "" {
		fmt.Fprintf(&b, "Pipeline run: %s\n", alert.PipelineURL)
	}
	// Header injection isn't possible through the SES API, but keep the subject on one line anyway.
	subject = "[Bogform] " + strings.Join(strings.Fields(alert.Summary), " ")
	return subject, b.String()
}
