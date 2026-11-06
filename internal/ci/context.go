// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package ci implements the "bogform ci" commands, which run Bogform from repository pipelines
// and report the results on pull requests.
package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Names of the supported pipeline platforms.
const (
	PlatformNone      = "none"
	PlatformGitHub    = "github"
	PlatformGitLab    = "gitlab"
	PlatformAzure     = "azure"
	PlatformBitbucket = "bitbucket"
)

// TokenEnvName is the variable holding the token used to post pull request comments. It's removed
// from the environment of the commands Bogform runs, so that providers and configuration can't read it.
const TokenEnvName = "BOG_CI_TOKEN"

// Context describes the pipeline run that Bogform is executing in.
type Context struct {
	Platform      string `json:"platform"`
	Repository    string `json:"repository,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Commit        string `json:"commit,omitempty"`
	RunURL        string `json:"run_url,omitempty"`
	IsPullRequest bool   `json:"is_pull_request"`
	PullRequest   string `json:"pull_request,omitempty"`
	FromFork      bool   `json:"from_fork,omitempty"`

	// apiURL is the base URL of the platform's API.
	apiURL string
	// projectURL identifies the project on platforms whose API paths need more than the repository.
	projectURL string
	// summaryPath is a file whose contents the platform shows on the run's page.
	summaryPath string
}

// EnvFunc looks up an environment variable. os.Getenv satisfies it.
type EnvFunc func(string) string

// Detect works out the current pipeline platform from its environment variables.
func Detect(env EnvFunc) Context {
	switch {
	case env("GITHUB_ACTIONS") == "true":
		return detectGitHub(env)
	case env("GITLAB_CI") != "":
		return detectGitLab(env)
	case strings.EqualFold(env("TF_BUILD"), "true") && env("SYSTEM_COLLECTIONURI") != "":
		return detectAzure(env)
	case env("BITBUCKET_BUILD_NUMBER") != "":
		return detectBitbucket(env)
	}
	return Context{Platform: PlatformNone}
}

func detectGitHub(env EnvFunc) Context {
	server := strings.TrimSuffix(env("GITHUB_SERVER_URL"), "/")
	repository := env("GITHUB_REPOSITORY")
	ctx := Context{
		Platform:    PlatformGitHub,
		Repository:  repository,
		Branch:      env("GITHUB_REF_NAME"),
		Commit:      env("GITHUB_SHA"),
		apiURL:      strings.TrimSuffix(env("GITHUB_API_URL"), "/"),
		summaryPath: env("GITHUB_STEP_SUMMARY"),
	}
	if runID := env("GITHUB_RUN_ID"); server != "" && repository != "" && runID != "" {
		ctx.RunURL = fmt.Sprintf("%s/%s/actions/runs/%s", server, repository, runID)
	}
	switch env("GITHUB_EVENT_NAME") {
	case "pull_request", "pull_request_target":
		ctx.IsPullRequest = true
		ctx.Branch = env("GITHUB_HEAD_REF")
		number, fromFork, err := readGitHubEvent(env("GITHUB_EVENT_PATH"))
		if err == nil && number != 0 {
			ctx.PullRequest = strconv.Itoa(number)
			ctx.FromFork = fromFork
		}
	}
	return ctx
}

// readGitHubEvent reads the pull request's number from the event payload that GitHub provides.
func readGitHubEvent(path string) (number int, fromFork bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	var event struct {
		PullRequest struct {
			Number int `json:"number"`
			Head   struct {
				Repo struct {
					Fork bool `json:"fork"`
				} `json:"repo"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return 0, false, err
	}
	return event.PullRequest.Number, event.PullRequest.Head.Repo.Fork, nil
}

func detectGitLab(env EnvFunc) Context {
	ctx := Context{
		Platform:   PlatformGitLab,
		Repository: env("CI_PROJECT_PATH"),
		Branch:     env("CI_COMMIT_REF_NAME"),
		Commit:     env("CI_COMMIT_SHA"),
		RunURL:     env("CI_JOB_URL"),
		apiURL:     strings.TrimSuffix(env("CI_API_V4_URL"), "/"),
		projectURL: env("CI_PROJECT_ID"),
	}
	if iid := env("CI_MERGE_REQUEST_IID"); iid != "" {
		ctx.IsPullRequest = true
		ctx.PullRequest = iid
		ctx.Branch = env("CI_MERGE_REQUEST_SOURCE_BRANCH_NAME")
		ctx.FromFork = env("CI_MERGE_REQUEST_SOURCE_PROJECT_ID") != "" &&
			env("CI_MERGE_REQUEST_SOURCE_PROJECT_ID") != env("CI_MERGE_REQUEST_PROJECT_ID")
	}
	return ctx
}

func detectAzure(env EnvFunc) Context {
	collection := env("SYSTEM_COLLECTIONURI")
	if !strings.HasSuffix(collection, "/") {
		collection += "/"
	}
	project := env("SYSTEM_TEAMPROJECT")
	ctx := Context{
		Platform:   PlatformAzure,
		Repository: env("BUILD_REPOSITORY_NAME"),
		Branch:     env("BUILD_SOURCEBRANCHNAME"),
		Commit:     env("BUILD_SOURCEVERSION"),
		apiURL:     collection,
		projectURL: project + "/" + env("BUILD_REPOSITORY_ID"),
	}
	if buildID := env("BUILD_BUILDID"); buildID != "" {
		ctx.RunURL = fmt.Sprintf("%s%s/_build/results?buildId=%s", collection, project, buildID)
	}
	if id := env("SYSTEM_PULLREQUEST_PULLREQUESTID"); id != "" {
		ctx.IsPullRequest = true
		ctx.PullRequest = id
		ctx.Branch = strings.TrimPrefix(env("SYSTEM_PULLREQUEST_SOURCEBRANCH"), "refs/heads/")
		ctx.FromFork = strings.EqualFold(env("SYSTEM_PULLREQUEST_ISFORK"), "true")
	}
	return ctx
}

func detectBitbucket(env EnvFunc) Context {
	repository := env("BITBUCKET_REPO_FULL_NAME")
	ctx := Context{
		Platform:   PlatformBitbucket,
		Repository: repository,
		Branch:     env("BITBUCKET_BRANCH"),
		Commit:     env("BITBUCKET_COMMIT"),
		apiURL:     "https://api.bitbucket.org/2.0",
	}
	if env("BITBUCKET_GIT_HTTP_ORIGIN") != "" && env("BITBUCKET_BUILD_NUMBER") != "" {
		ctx.RunURL = fmt.Sprintf("%s/addon/pipelines/home#!/results/%s", env("BITBUCKET_GIT_HTTP_ORIGIN"), env("BITBUCKET_BUILD_NUMBER"))
	}
	if id := env("BITBUCKET_PR_ID"); id != "" {
		ctx.IsPullRequest = true
		ctx.PullRequest = id
	}
	return ctx
}
