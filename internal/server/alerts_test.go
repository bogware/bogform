// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogware/bogform/internal/ci"
)

func TestAlertEvent(t *testing.T) {
	report := func(kind, status string, pullRequest bool) *ci.Report {
		r := testReport("acme/infra")
		r.Kind, r.Status, r.Run.IsPullRequest = kind, status, pullRequest
		return r
	}
	tests := map[string]struct {
		report   *ci.Report
		previous string
		want     string
	}{
		"applied":                    {report(ci.KindApply, ci.StatusApplied, false), "", EventApplySucceeded},
		"apply with nothing to do":   {report(ci.KindApply, ci.StatusNoChanges, false), "", ""},
		"apply failed":               {report(ci.KindApply, ci.StatusFailed, false), "", EventApplyFailed},
		"plan failed":                {report(ci.KindPlan, ci.StatusFailed, true), "", EventPlanFailed},
		"plan with changes":          {report(ci.KindPlan, ci.StatusChanges, true), "", ""},
		"drift starts":               {report(ci.KindDrift, ci.StatusDrifted, false), "", EventDriftDetected},
		"drift continues":            {report(ci.KindDrift, ci.StatusDrifted, false), ci.StatusDrifted, ""},
		"drift ends":                 {report(ci.KindDrift, ci.StatusNoChanges, false), ci.StatusDrifted, EventDriftResolved},
		"no drift, still":            {report(ci.KindDrift, ci.StatusNoChanges, false), ci.StatusNoChanges, ""},
		"drift on a pull request":    {report(ci.KindDrift, ci.StatusDrifted, true), "", ""},
		"drift check that failed":    {report(ci.KindDrift, ci.StatusFailed, false), ci.StatusDrifted, ""},
		"drift with no earlier info": {report(ci.KindDrift, ci.StatusNoChanges, false), "", ""},
	}
	for name, test := range tests {
		if got := alertEvent(test.report, test.previous); got != test.want {
			t.Errorf("%s: got %q, want %q", name, got, test.want)
		}
	}
}

func testAlert() Alert {
	report := testReport("acme/infra")
	report.Kind, report.Status = ci.KindApply, ci.StatusFailed
	report.Run.IsPullRequest, report.Run.PullRequest, report.Run.Branch = false, "", "main"
	report.Run.Commit, report.Run.Actor, report.Run.Trigger = "0123456789abcdef", "octocat", "workflow_dispatch"
	report.Counts = ci.Counts{Add: 2, Destroy: 1}
	report.Error = "Error: <creating> bucket & policy"
	return newAlert(EventApplyFailed, report, "https://bogform.example.com/runs/7")
}

func TestNewAlert(t *testing.T) {
	alert := testAlert()
	if alert.Summary != "Apply failed: acme/infra (infra, default)" {
		t.Errorf("summary: %q", alert.Summary)
	}
	if got := alert.source(); got != "main at 01234567 by octocat (workflow_dispatch)" {
		t.Errorf("source: %q", got)
	}
	if got := alert.changes(); got != "2 to add, 1 to destroy" {
		t.Errorf("changes: %q", got)
	}
	alert.Event = EventApplySucceeded
	if got := alert.changes(); got != "2 added, 1 destroyed" {
		t.Errorf("applied changes: %q", got)
	}

	long := testReport("acme/infra")
	long.Error = strings.Repeat("é", maxAlertError)
	if got := newAlert(EventPlanFailed, long, "").Error; len(got) > maxAlertError+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("a long error wasn't shortened: %d bytes", len(got))
	}
}

func TestAlertConfigWants(t *testing.T) {
	cfg := AlertConfig{Events: []string{EventApplyFailed}, Repositories: []string{"acme/*"}}
	for _, test := range []struct {
		event, repository string
		want              bool
	}{
		{EventApplyFailed, "acme/infra", true},
		{EventApplyFailed, "other/infra", false},
		{EventPlanFailed, "acme/infra", false},
	} {
		if got := cfg.wants(test.event, test.repository); got != test.want {
			t.Errorf("wants(%q, %q) = %v", test.event, test.repository, got)
		}
	}
	if !(AlertConfig{}).wants(EventDriftDetected, "anything") {
		t.Error("an empty filter should allow everything")
	}
}

func TestAlertConfigValidate(t *testing.T) {
	tests := map[string]struct {
		cfg  AlertConfig
		want string
	}{
		"fine":            {AlertConfig{SlackWebhook: "https://hooks.slack.com/x", WebhookURL: "https://example.com", WebhookSecret: "s"}, ""},
		"unknown event":   {AlertConfig{Events: []string{"apply_maybe"}}, "isn't an event"},
		"bad pattern":     {AlertConfig{Repositories: []string{"["}}, "isn't a valid pattern"},
		"not a URL":       {AlertConfig{TeamsWebhook: "teams.example.com"}, "http or https"},
		"placeholder":     {AlertConfig{SlackWebhook: PlaceholderSecret}, "placeholder"},
		"email half set":  {AlertConfig{EmailFrom: "bogform@example.com"}, "set together"},
		"unsigned hook":   {AlertConfig{WebhookURL: "https://example.com"}, "WEBHOOK_SECRET is required"},
		"placeholder key": {AlertConfig{WebhookURL: "https://example.com", WebhookSecret: PlaceholderSecret}, "WEBHOOK_SECRET is required"},
	}
	for name, test := range tests {
		var problems []string
		test.cfg.validate(func(format string, args ...any) { problems = append(problems, format) })
		joined := strings.Join(problems, "\n")
		if test.want == "" && joined != "" {
			t.Errorf("%s: unexpected problems: %s", name, joined)
		}
		if test.want != "" && !strings.Contains(joined, test.want) {
			t.Errorf("%s: got %q, want it to mention %q", name, joined, test.want)
		}
	}
}

func TestLoadConfig_alerts(t *testing.T) {
	env := map[string]string{
		"BOG_SERVER_PUBLIC_URL":           "https://bogform.example.com",
		"BOG_SERVER_DATABASE_URL":         "postgres://x",
		"BOG_SERVER_SESSION_KEY":          strings.Repeat("ab", 32),
		"BOG_SERVER_VIEWERS":              "@example.com",
		"BOG_SERVER_OIDC_ISSUER":          "https://idp.example.com",
		"BOG_SERVER_OIDC_CLIENT_ID":       "id",
		"BOG_SERVER_OIDC_CLIENT_SECRET":   "secret",
		"BOG_SERVER_ALERT_EMAIL_FROM":     "bogform@example.com",
		"BOG_SERVER_ALERT_EMAIL_TO":       "Ops@example.com, oncall@example.com",
		"BOG_SERVER_ALERT_EVENTS":         "apply_failed,drift_detected",
		"BOG_SERVER_ALERT_REPOSITORIES":   "Acme/*",
		"BOG_SERVER_ALERT_SLACK_WEBHOOK":  "https://hooks.slack.com/services/x",
		"BOG_SERVER_ALERT_WEBHOOK_URL":    "https://hooks.example.com/bogform",
		"BOG_SERVER_ALERT_WEBHOOK_SECRET": "shh",
	}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Alerts
	if strings.Join(got.EmailTo, ",") != "ops@example.com,oncall@example.com" || strings.Join(got.Events, ",") != "apply_failed,drift_detected" {
		t.Errorf("lists weren't read: %+v", got)
	}
	if strings.Join(got.Repositories, ",") != "Acme/*" {
		t.Errorf("repository patterns must keep their case: %v", got.Repositories)
	}
	if !got.enabled() {
		t.Error("alerts should be enabled")
	}
}

func TestWebhookSignature(t *testing.T) {
	body := []byte(`{"event":"apply_failed"}`)
	now := time.Unix(1_800_000_000, 0)
	signature := SignWebhook("secret", now.Unix(), body)
	if !strings.HasPrefix(signature, "sha256=") || len(signature) != len("sha256=")+64 {
		t.Fatalf("unexpected signature format %q", signature)
	}
	timestamp := "1800000000"
	if !VerifyWebhook("secret", signature, timestamp, body, now, 5*time.Minute) {
		t.Error("a valid signature didn't verify")
	}
	if VerifyWebhook("other", signature, timestamp, body, now, 5*time.Minute) {
		t.Error("the wrong secret verified")
	}
	if VerifyWebhook("secret", signature, timestamp, []byte(`{"event":"applied"}`), now, 5*time.Minute) {
		t.Error("a changed body verified")
	}
	if VerifyWebhook("secret", signature, "1800000001", body, now, 5*time.Minute) {
		t.Error("a changed timestamp verified")
	}
	if VerifyWebhook("secret", signature, timestamp, body, now.Add(10*time.Minute), 5*time.Minute) {
		t.Error("an old signature verified")
	}
}

type fakeEmail struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeEmail) Send(_ context.Context, from string, to []string, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, from+"|"+strings.Join(to, ",")+"|"+subject+"|"+body)
	return nil
}

type received struct {
	header http.Header
	body   []byte
}

func recordingServer(t *testing.T, statuses ...int) (*httptest.Server, func() []received) {
	t.Helper()
	var mu sync.Mutex
	var got []received
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, received{header: r.Header.Clone(), body: body})
		mu.Unlock()
		n := int(calls.Add(1)) - 1
		if n < len(statuses) {
			w.WriteHeader(statuses[n])
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), got...)
	}
}

func testAlerter(cfg AlertConfig, email emailSender) (*alerter, *[]string) {
	var logs []string
	var mu sync.Mutex
	a := newAlerter(cfg, email, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, format)
	})
	a.retry = time.Millisecond
	return a, &logs
}

func TestAlerter_sendsToEveryDestination(t *testing.T) {
	slack, slackGot := recordingServer(t)
	teams, teamsGot := recordingServer(t)
	hook, hookGot := recordingServer(t)
	email := &fakeEmail{}
	a, logs := testAlerter(AlertConfig{
		SlackWebhook: slack.URL, TeamsWebhook: teams.URL,
		WebhookURL: hook.URL, WebhookSecret: "shh",
		EmailFrom: "bogform@example.com", EmailTo: []string{"ops@example.com"},
	}, email)
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	a.notify(testAlert())
	a.wait()
	if len(*logs) > 0 {
		t.Fatalf("unexpected errors: %v", *logs)
	}

	var slackBody struct {
		Text   string `json:"text"`
		Blocks []struct {
			Text struct{ Text string } `json:"text"`
		} `json:"blocks"`
	}
	if got := slackGot(); len(got) != 1 || json.Unmarshal(got[0].body, &slackBody) != nil {
		t.Fatalf("Slack got %d requests", len(got))
	}
	section := slackBody.Blocks[0].Text.Text
	for _, want := range []string{"*Apply failed: acme/infra (infra, default)*", "&lt;creating&gt; bucket &amp; policy", "<https://bogform.example.com/runs/7|Dashboard>", "*Changes:* 2 to add, 1 to destroy"} {
		if !strings.Contains(section, want) {
			t.Errorf("Slack message is missing %q:\n%s", want, section)
		}
	}

	var teamsBody struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type    string           `json:"type"`
				Body    []map[string]any `json:"body"`
				Actions []map[string]any `json:"actions"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if got := teamsGot(); len(got) != 1 || json.Unmarshal(got[0].body, &teamsBody) != nil {
		t.Fatalf("Teams got %d requests", len(got))
	}
	card := teamsBody.Attachments[0]
	if teamsBody.Type != "message" || card.ContentType != "application/vnd.microsoft.card.adaptive" || card.Content.Type != "AdaptiveCard" {
		t.Errorf("not an Adaptive Card message: %+v", teamsBody)
	}
	if card.Content.Body[0]["color"] != "Attention" || len(card.Content.Actions) != 2 {
		t.Errorf("unexpected card: %+v", card.Content)
	}

	got := hookGot()
	if len(got) != 1 {
		t.Fatalf("the webhook got %d requests", len(got))
	}
	header := got[0].header
	if !VerifyWebhook("shh", header.Get(headerSignature), header.Get(headerTimestamp), got[0].body, now, time.Minute) {
		t.Error("the webhook's signature didn't verify")
	}
	if header.Get(headerEvent) != EventApplyFailed || header.Get("Content-Type") != "application/json" {
		t.Errorf("unexpected headers: %v", header)
	}
	var alert Alert
	if err := json.Unmarshal(got[0].body, &alert); err != nil || alert.Repository != "acme/infra" || alert.Counts.Add != 2 || alert.Actor != "octocat" {
		t.Errorf("unexpected webhook body: %s", got[0].body)
	}

	if len(email.sent) != 1 {
		t.Fatalf("sent %d emails", len(email.sent))
	}
	parts := strings.SplitN(email.sent[0], "|", 4)
	if parts[0] != "bogform@example.com" || parts[1] != "ops@example.com" || parts[2] != "[Bogform] Apply failed: acme/infra (infra, default)" {
		t.Errorf("unexpected email: %q", email.sent[0])
	}
	if !strings.Contains(parts[3], "Dashboard: https://bogform.example.com/runs/7") || !strings.Contains(parts[3], "From: main at 01234567 by octocat") {
		t.Errorf("unexpected email body:\n%s", parts[3])
	}
}

func TestAlerter_retriesOnceOnServerErrors(t *testing.T) {
	srv, got := recordingServer(t, http.StatusBadGateway, http.StatusOK)
	a, logs := testAlerter(AlertConfig{SlackWebhook: srv.URL}, nil)
	a.notify(testAlert())
	a.wait()
	if n := len(got()); n != 2 {
		t.Errorf("got %d requests, want 2", n)
	}
	if len(*logs) != 0 {
		t.Errorf("a retry that worked shouldn't log: %v", *logs)
	}
}

func TestAlerter_doesNotRetryRejections(t *testing.T) {
	srv, got := recordingServer(t, http.StatusForbidden, http.StatusOK)
	a, logs := testAlerter(AlertConfig{SlackWebhook: srv.URL}, nil)
	a.notify(testAlert())
	a.wait()
	if n := len(got()); n != 1 {
		t.Errorf("got %d requests, want 1", n)
	}
	if len(*logs) != 1 {
		t.Errorf("a failure should be logged once, got %v", *logs)
	}
}

func TestAlerter_timesOutAndKeepsURLsOutOfErrors(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	t.Cleanup(func() { close(block); srv.Close() })

	var mu sync.Mutex
	var logged []string
	a := newAlerter(AlertConfig{SlackWebhook: srv.URL + "/services/T000/SECRET"}, nil, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		for _, arg := range args {
			if err, ok := arg.(error); ok {
				logged = append(logged, err.Error())
			}
		}
	})
	a.client.Timeout = 50 * time.Millisecond
	a.retry = time.Millisecond
	start := time.Now()
	a.notify(testAlert())
	a.wait()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waiting took %s", elapsed)
	}
	if len(logged) != 1 || strings.Contains(logged[0], "SECRET") {
		t.Errorf("unexpected log: %v", logged)
	}
}

func TestAlerter_filtersAndNil(t *testing.T) {
	srv, got := recordingServer(t)
	a, _ := testAlerter(AlertConfig{SlackWebhook: srv.URL, Events: []string{EventDriftDetected}}, nil)
	a.notify(testAlert())
	a.wait()
	if n := len(got()); n != 0 {
		t.Errorf("a filtered event was sent %d times", n)
	}

	var none *alerter
	none.notify(testAlert()) // Must not panic when alerts aren't configured.
	none.wait()
}

func TestRetryable(t *testing.T) {
	for err, want := range map[error]bool{
		&statusError{code: 500}:  true,
		&statusError{code: 429}:  true,
		&statusError{code: 404}:  false,
		errors.New("refused"):    true,
		&statusError{code: 400}:  false,
		&statusError{code: 503}:  true,
		&statusError{code: 3000}: true,
	} {
		if got := retryable(err); got != want {
			t.Errorf("retryable(%v) = %v", err, got)
		}
	}
}

func TestReports_sendAlerts(t *testing.T) {
	db := testDB(t)
	hook, got := recordingServer(t)
	cfg := testConfig()
	cfg.Alerts = AlertConfig{WebhookURL: hook.URL, WebhookSecret: "shh"}
	s := testServer(t, cfg, db)
	handler := s.Handler()
	token, err := CreateToken(t.Context(), db, "acme", "acme/*")
	if err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	send := func(kind, status string) {
		t.Helper()
		report := testReport("acme/infra")
		report.Kind, report.Status = kind, status
		report.Run.IsPullRequest, report.Run.PullRequest, report.Run.Branch = false, "", "main"
		at = at.Add(time.Minute)
		report.StartedAt, report.FinishedAt = at.Add(-time.Second), at
		if w := postReport(t, handler, token, report); w.Code != http.StatusCreated {
			t.Fatalf("%s %s: %d %s", kind, status, w.Code, w.Body)
		}
		s.Wait()
	}
	send(ci.KindPlan, ci.StatusChanges)    // Nothing to say.
	send(ci.KindDrift, ci.StatusDrifted)   // Drift starts.
	send(ci.KindDrift, ci.StatusDrifted)   // Still drifted, so no repeat.
	send(ci.KindDrift, ci.StatusNoChanges) // Drift ends.
	send(ci.KindApply, ci.StatusFailed)    // Apply failed.
	send(ci.KindApply, ci.StatusApplied)   // Applied.
	send(ci.KindApply, ci.StatusNoChanges) // Nothing to say.

	var events []string
	for _, request := range got() {
		var alert Alert
		if err := json.Unmarshal(request.body, &alert); err != nil {
			t.Fatal(err)
		}
		events = append(events, alert.Event)
		if !strings.HasPrefix(alert.DashboardURL, "https://bogform.example.com/runs/") {
			t.Errorf("unexpected dashboard link %q", alert.DashboardURL)
		}
	}
	want := []string{EventDriftDetected, EventDriftResolved, EventApplyFailed, EventApplySucceeded}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("got events %v, want %v", events, want)
	}
}
