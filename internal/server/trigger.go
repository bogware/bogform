// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/bogware/bogform/internal/ci"
)

func (t TriggerConfig) supports(workspace Workspace) bool {
	switch workspace.Platform {
	case ci.PlatformGitHub:
		return t.GitHubToken != ""
	case ci.PlatformGitLab:
		return t.GitLabToken != ""
	case ci.PlatformBitbucket:
		return t.BitbucketToken != ""
	case ci.PlatformAzure:
		_, mapped := t.AzurePipelines[workspace.Repository]
		return t.AzureToken != "" && t.AzureOrgURL != "" && mapped
	}
	return false
}

// start asks the platform to run the workspace's pipeline on its default branch. The pipeline's own
// approvals still apply, so starting a run can't by itself change any infrastructure.
func (t TriggerConfig) start(ctx context.Context, client *http.Client, workspace Workspace) error {
	repository, ref := workspace.Repository, t.Ref
	if !validRef.MatchString(ref) {
		return fmt.Errorf("the ref %q isn't valid", ref)
	}
	escaped := escapePath(repository)
	switch workspace.Platform {
	case ci.PlatformGitHub:
		target := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches", t.GitHubAPIURL, escaped, url.PathEscape(t.GitHubWorkflow))
		return send(ctx, client, target, map[string]string{"Authorization": "Bearer " + t.GitHubToken, "Accept": "application/vnd.github+json"},
			map[string]any{"ref": ref})
	case ci.PlatformGitLab:
		target := fmt.Sprintf("%s/projects/%s/pipeline", t.GitLabAPIURL, url.PathEscape(repository))
		return send(ctx, client, target, map[string]string{"PRIVATE-TOKEN": t.GitLabToken}, map[string]any{"ref": ref})
	case ci.PlatformBitbucket:
		target := fmt.Sprintf("%s/repositories/%s/pipelines/", t.BitbucketURL, escaped)
		return send(ctx, client, target, map[string]string{"Authorization": "Bearer " + t.BitbucketToken},
			map[string]any{"target": map[string]string{"type": "pipeline_ref_target", "ref_type": "branch", "ref_name": ref}})
	case ci.PlatformAzure:
		project, pipeline, found := strings.Cut(t.AzurePipelines[repository], "/")
		if !found {
			return fmt.Errorf("no pipeline is configured for %s", repository)
		}
		target := fmt.Sprintf("%s/%s/_apis/pipelines/%s/runs?api-version=7.1", t.AzureOrgURL, url.PathEscape(project), url.PathEscape(pipeline))
		// Azure DevOps personal access tokens use basic authentication with an empty user name.
		basic := base64.StdEncoding.EncodeToString([]byte(":" + t.AzureToken))
		return send(ctx, client, target, map[string]string{"Authorization": "Basic " + basic},
			map[string]any{"resources": map[string]any{"repositories": map[string]any{"self": map[string]string{"refName": "refs/heads/" + ref}}}})
	}
	return fmt.Errorf("unsupported platform %q", workspace.Platform)
}

// escapePath escapes each segment of a repository name for use in a URL.
func escapePath(repository string) string {
	segments := strings.Split(repository, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func send(ctx context.Context, client *http.Client, target string, headers map[string]string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bogform-server")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// Only the usual success codes count. Azure DevOps answers a bad token with 203 and a sign-in page.
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
	default:
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		return fmt.Errorf("%s returned %d: %s", req.URL.Redacted(), res.StatusCode, detail)
	}
	return nil
}
