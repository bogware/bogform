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
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Commenter creates or updates a single comment on the current pull request.
type Commenter interface {
	// Upsert updates the comment containing marker, or creates one if there isn't any.
	Upsert(ctx context.Context, marker, body string) error
	// MaxBody is the longest comment that the platform accepts, in bytes.
	MaxBody() int
	// Collapsible reports whether the platform renders HTML <details> blocks.
	Collapsible() bool
}

// ErrNoCommenter means that the current run can't comment, for example outside a pull request.
var ErrNoCommenter = errors.New("this run has no pull request to comment on")

// NewCommenter returns the Commenter for the current pipeline platform.
func NewCommenter(run Context, token string, client *http.Client) (Commenter, error) {
	if !run.IsPullRequest || run.PullRequest == "" {
		return nil, ErrNoCommenter
	}
	if token == "" {
		return nil, fmt.Errorf("%s isn't set", TokenEnvName)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	api := &apiClient{client: client}
	switch run.Platform {
	case PlatformGitHub:
		api.setHeaders = func(h http.Header) {
			h.Set("Authorization", "Bearer "+token)
			h.Set("Accept", "application/vnd.github+json")
			h.Set("X-GitHub-Api-Version", "2022-11-28")
		}
		return &githubCommenter{api: api, base: run.apiURL, repo: run.Repository, number: run.PullRequest}, nil
	case PlatformGitLab:
		api.setHeaders = func(h http.Header) { h.Set("PRIVATE-TOKEN", token) }
		return &gitlabCommenter{api: api, base: run.apiURL, project: run.projectURL, iid: run.PullRequest}, nil
	case PlatformAzure:
		api.setHeaders = func(h http.Header) { h.Set("Authorization", "Bearer "+token) }
		project, repo, _ := strings.Cut(run.projectURL, "/")
		return &azureCommenter{
			api:        api,
			base:       fmt.Sprintf("%s%s/_apis/git/repositories/%s/pullRequests/%s/threads", run.apiURL, url.PathEscape(project), url.PathEscape(repo), run.PullRequest),
			connection: run.apiURL + "_apis/connectionData?api-version=7.1-preview.1",
		}, nil
	case PlatformBitbucket:
		api.setHeaders = func(h http.Header) { h.Set("Authorization", "Bearer "+token) }
		return &bitbucketCommenter{
			api:  api,
			base: fmt.Sprintf("%s/repositories/%s/pullrequests/%s/comments", run.apiURL, run.Repository, run.PullRequest),
			user: run.apiURL + "/user",
		}, nil
	}
	return nil, ErrNoCommenter
}

// apiClient makes JSON API requests, retrying a few times if the service is temporarily unavailable.
type apiClient struct {
	client     *http.Client
	setHeaders func(http.Header)
	retryDelay time.Duration
}

// maxResponseBytes limits how much of a response is read, because comment lists can be long.
const maxResponseBytes = 32 << 20

func (a *apiClient) do(ctx context.Context, method, target string, in, out any) error {
	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return err
		}
	}
	delay := a.retryDelay
	if delay == 0 {
		delay = 2 * time.Second
	}
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay * time.Duration(attempt)):
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		a.setHeaders(req.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "bogform-ci")

		res, err := a.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
		res.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastErr = apiError(method, target, res.StatusCode, body)
			continue
		}
		if res.StatusCode >= 300 {
			return apiError(method, target, res.StatusCode, body)
		}
		if out != nil && len(body) > 0 {
			return json.Unmarshal(body, out)
		}
		return nil
	}
	return lastErr
}

func apiError(method, target string, status int, body []byte) error {
	// The query string and the response are safe to show, but keep the message short.
	message := strings.TrimSpace(string(body))
	if len(message) > 300 {
		message = message[:300] + "..."
	}
	return fmt.Errorf("%s %s returned %d: %s", method, target, status, message)
}

// GitHub Actions

type githubCommenter struct {
	api                *apiClient
	base, repo, number string
}

func (*githubCommenter) MaxBody() int      { return 65000 }
func (*githubCommenter) Collapsible() bool { return true }

// ownsComment decides whether a comment can be updated. The marker is predictable, so anyone could write
// a comment containing it. If the bot then wrote the plan into that comment, its author could edit it to
// mislead reviewers. Output from another directory's plan could also contain the marker, so it must be at
// the very start.
func ownsComment(body, marker string, author, self any) bool {
	return strings.HasPrefix(body, marker) && self != nil && author == self
}

func (g *githubCommenter) Upsert(ctx context.Context, marker, body string) error {
	list := fmt.Sprintf("%s/repos/%s/issues/%s/comments", g.base, g.repo, g.number)
	// The token that GitHub Actions provides can't call /user, and always comments as this account.
	self := "github-actions[bot]"
	var user struct {
		Login string `json:"login"`
	}
	if err := g.api.do(ctx, http.MethodGet, g.base+"/user", nil, &user); err == nil && user.Login != "" {
		self = user.Login
	}
	for page := 1; ; page++ {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := g.api.do(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", list, page), nil, &comments); err != nil {
			return err
		}
		for _, comment := range comments {
			if ownsComment(comment.Body, marker, comment.User.Login, self) {
				target := fmt.Sprintf("%s/repos/%s/issues/comments/%d", g.base, g.repo, comment.ID)
				return g.api.do(ctx, http.MethodPatch, target, map[string]string{"body": body}, nil)
			}
		}
		if len(comments) < 100 {
			break
		}
	}
	return g.api.do(ctx, http.MethodPost, list, map[string]string{"body": body}, nil)
}

// GitLab CI

type gitlabCommenter struct {
	api                *apiClient
	base, project, iid string
}

func (*gitlabCommenter) MaxBody() int      { return 900000 }
func (*gitlabCommenter) Collapsible() bool { return true }

func (g *gitlabCommenter) Upsert(ctx context.Context, marker, body string) error {
	list := fmt.Sprintf("%s/projects/%s/merge_requests/%s/notes", g.base, url.PathEscape(g.project), g.iid)
	// If the token's identity can't be found, nothing matches and a new comment is created, which is safe.
	var self any
	var user struct {
		ID int64 `json:"id"`
	}
	if err := g.api.do(ctx, http.MethodGet, g.base+"/user", nil, &user); err == nil && user.ID != 0 {
		self = user.ID
	}
	for page := 1; ; page++ {
		var notes []struct {
			ID     int64  `json:"id"`
			Body   string `json:"body"`
			Author struct {
				ID int64 `json:"id"`
			} `json:"author"`
		}
		if err := g.api.do(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", list, page), nil, &notes); err != nil {
			return err
		}
		for _, note := range notes {
			if ownsComment(note.Body, marker, note.Author.ID, self) {
				return g.api.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", list, note.ID), map[string]string{"body": body}, nil)
			}
		}
		if len(notes) < 100 {
			break
		}
	}
	return g.api.do(ctx, http.MethodPost, list, map[string]string{"body": body}, nil)
}

// Azure Pipelines

const azureAPIVersion = "api-version=7.1"

type azureCommenter struct {
	api        *apiClient
	base       string
	connection string
}

func (*azureCommenter) MaxBody() int      { return 140000 }
func (*azureCommenter) Collapsible() bool { return true }

func (a *azureCommenter) Upsert(ctx context.Context, marker, body string) error {
	var self any
	var connection struct {
		AuthenticatedUser struct {
			ID string `json:"id"`
		} `json:"authenticatedUser"`
	}
	if err := a.api.do(ctx, http.MethodGet, a.connection, nil, &connection); err == nil && connection.AuthenticatedUser.ID != "" {
		self = connection.AuthenticatedUser.ID
	}
	var threads struct {
		Value []struct {
			ID       int64 `json:"id"`
			Comments []struct {
				ID      int64  `json:"id"`
				Content string `json:"content"`
				Author  struct {
					ID string `json:"id"`
				} `json:"author"`
			} `json:"comments"`
		} `json:"value"`
	}
	if err := a.api.do(ctx, http.MethodGet, a.base+"?"+azureAPIVersion, nil, &threads); err != nil {
		return err
	}
	for _, thread := range threads.Value {
		for _, comment := range thread.Comments {
			if ownsComment(comment.Content, marker, comment.Author.ID, self) {
				target := fmt.Sprintf("%s/%d/comments/%d?%s", a.base, thread.ID, comment.ID, azureAPIVersion)
				return a.api.do(ctx, http.MethodPatch, target, map[string]string{"content": body}, nil)
			}
		}
	}
	thread := map[string]any{
		"status":   "closed", // A report isn't something a reviewer must resolve.
		"comments": []map[string]any{{"parentCommentId": 0, "content": body, "commentType": 1}},
	}
	return a.api.do(ctx, http.MethodPost, a.base+"?"+azureAPIVersion, thread, nil)
}

// Bitbucket Pipelines

type bitbucketCommenter struct {
	api  *apiClient
	base string
	user string
}

func (*bitbucketCommenter) MaxBody() int      { return 32000 }
func (*bitbucketCommenter) Collapsible() bool { return false }

func (b *bitbucketCommenter) Upsert(ctx context.Context, marker, body string) error {
	content := map[string]any{"content": map[string]string{"raw": body}}
	var self any
	var user struct {
		UUID string `json:"uuid"`
	}
	if err := b.api.do(ctx, http.MethodGet, b.user, nil, &user); err == nil && user.UUID != "" {
		self = user.UUID
	}
	next := b.base + "?pagelen=100"
	for next != "" {
		var page struct {
			Next   string `json:"next"`
			Values []struct {
				ID      int64 `json:"id"`
				Content struct {
					Raw string `json:"raw"`
				} `json:"content"`
				User struct {
					UUID string `json:"uuid"`
				} `json:"user"`
			} `json:"values"`
		}
		if err := b.api.do(ctx, http.MethodGet, next, nil, &page); err != nil {
			return err
		}
		for _, comment := range page.Values {
			if ownsComment(comment.Content.Raw, marker, comment.User.UUID, self) {
				return b.api.do(ctx, http.MethodPut, b.base+"/"+strconv.FormatInt(comment.ID, 10), content, nil)
			}
		}
		next = page.Next
	}
	return b.api.do(ctx, http.MethodPost, b.base, content, nil)
}
