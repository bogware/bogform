// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package tfcmigrate reads what's needed to move a workspace off HCP Terraform or Terraform
// Enterprise: its current state, its settings, and its variables. It only reads. Nothing in
// HCP Terraform is changed.
package tfcmigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxStateSize limits a downloaded state, so a misbehaving server can't exhaust memory.
const maxStateSize = 512 << 20

// ErrNoState means the workspace exists but has never stored any state.
var ErrNoState = errors.New("the workspace has no state")

// Client talks to the HCP Terraform API (v2) of one host.
type Client struct {
	// BaseURL is the API's root, such as https://app.terraform.io/api/v2.
	BaseURL string
	// Token is the API token. It's sent only to BaseURL's host.
	Token string
	// HTTP is the client used for requests. A default with a timeout is used if it's nil.
	HTTP *http.Client
}

// NewClient returns a client for a hostname such as app.terraform.io.
func NewClient(hostname, token string) *Client {
	return &Client{
		BaseURL: "https://" + hostname + "/api/v2",
		Token:   token,
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
	}
}

// Workspace is what matters about a workspace when moving it.
type Workspace struct {
	ID               string
	Name             string
	TerraformVersion string
	WorkingDirectory string
	ExecutionMode    string
	AutoApply        bool
	// VCSRepository is the connected repository, such as "bogware/example", or "" if there's none.
	VCSRepository string
	// VCSBranch is the branch runs come from. "" means the repository's default branch.
	VCSBranch string
}

// Variable is a workspace variable. Value is always "" for sensitive variables, which the API
// doesn't return.
type Variable struct {
	Key       string
	Value     string
	Category  string // "terraform" or "env"
	HCL       bool
	Sensitive bool
}

// StateVersion describes a state version without its contents.
type StateVersion struct {
	ID          string
	Serial      int64
	CreatedAt   time.Time
	DownloadURL string
}

type document[T any] struct {
	Data T `json:"data"`
}

type resource[T any] struct {
	ID         string `json:"id"`
	Attributes T      `json:"attributes"`
}

type workspaceAttributes struct {
	Name             string `json:"name"`
	TerraformVersion string `json:"terraform-version"`
	WorkingDirectory string `json:"working-directory"`
	ExecutionMode    string `json:"execution-mode"`
	AutoApply        bool   `json:"auto-apply"`
	VCSRepo          *struct {
		Identifier string `json:"identifier"`
		Branch     string `json:"branch"`
	} `json:"vcs-repo"`
}

type variableAttributes struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Category  string `json:"category"`
	HCL       bool   `json:"hcl"`
	Sensitive bool   `json:"sensitive"`
}

type stateVersionAttributes struct {
	Serial      int64     `json:"serial"`
	CreatedAt   time.Time `json:"created-at"`
	DownloadURL string    `json:"hosted-state-download-url"`
}

// statusError is a failed API response.
type statusError struct {
	Status int
	Path   string
}

func (e *statusError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "HCP Terraform rejected the token. Run \"bogform login\", or set a token for the host in BOG_TOKEN_<host>"
	case http.StatusNotFound:
		return fmt.Sprintf("not found: %s. Check the organization and workspace names, and that the token can read them", e.Path)
	default:
		return fmt.Sprintf("HCP Terraform returned %d for %s", e.Status, e.Path)
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// get fetches an absolute URL. The token is only attached for the API's own host, so it isn't
// sent to wherever a download URL might point.
func (c *Client) get(ctx context.Context, rawURL string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, 0, err
	}
	if req.URL.Host == base.Host && req.URL.Scheme == base.Scheme {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/vnd.api+json, application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, fmt.Errorf("the response from %s is larger than %d bytes", req.URL.Path, limit)
	}
	return body, resp.StatusCode, nil
}

func (c *Client) api(ctx context.Context, path string, out any) error {
	body, status, err := c.get(ctx, strings.TrimSuffix(c.BaseURL, "/")+path, 32<<20)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &statusError{Status: status, Path: path}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("couldn't read the response for %s: %w", path, err)
	}
	return nil
}

// Workspace looks up a workspace by organization and name.
func (c *Client) Workspace(ctx context.Context, organization, name string) (*Workspace, error) {
	var doc document[resource[workspaceAttributes]]
	path := "/organizations/" + url.PathEscape(organization) + "/workspaces/" + url.PathEscape(name)
	if err := c.api(ctx, path, &doc); err != nil {
		return nil, err
	}
	a := doc.Data.Attributes
	ws := &Workspace{
		ID:               doc.Data.ID,
		Name:             a.Name,
		TerraformVersion: a.TerraformVersion,
		WorkingDirectory: a.WorkingDirectory,
		ExecutionMode:    a.ExecutionMode,
		AutoApply:        a.AutoApply,
	}
	if a.VCSRepo != nil {
		ws.VCSRepository = a.VCSRepo.Identifier
		ws.VCSBranch = a.VCSRepo.Branch
	}
	return ws, nil
}

// Variables lists a workspace's own variables. Variables from variable sets aren't included.
func (c *Client) Variables(ctx context.Context, workspaceID string) ([]Variable, error) {
	var doc document[[]resource[variableAttributes]]
	if err := c.api(ctx, "/workspaces/"+url.PathEscape(workspaceID)+"/vars", &doc); err != nil {
		return nil, err
	}
	vars := make([]Variable, 0, len(doc.Data))
	for _, r := range doc.Data {
		v := Variable{
			Key:       r.Attributes.Key,
			Category:  r.Attributes.Category,
			HCL:       r.Attributes.HCL,
			Sensitive: r.Attributes.Sensitive,
		}
		if !v.Sensitive {
			v.Value = r.Attributes.Value
		}
		vars = append(vars, v)
	}
	return vars, nil
}

// CurrentStateVersion describes the workspace's latest state. It returns ErrNoState if there's none.
func (c *Client) CurrentStateVersion(ctx context.Context, workspaceID string) (*StateVersion, error) {
	var doc document[resource[stateVersionAttributes]]
	err := c.api(ctx, "/workspaces/"+url.PathEscape(workspaceID)+"/current-state-version", &doc)
	var se *statusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return nil, ErrNoState
	}
	if err != nil {
		return nil, err
	}
	return &StateVersion{
		ID:          doc.Data.ID,
		Serial:      doc.Data.Attributes.Serial,
		CreatedAt:   doc.Data.Attributes.CreatedAt,
		DownloadURL: doc.Data.Attributes.DownloadURL,
	}, nil
}

// DownloadState returns the raw state file of a state version.
func (c *Client) DownloadState(ctx context.Context, version *StateVersion) ([]byte, error) {
	if version.DownloadURL == "" {
		return nil, errors.New("the state version has no download URL. The token might not have permission to read state")
	}
	u, err := url.Parse(version.DownloadURL)
	if err != nil || (u.Scheme != "https" && !strings.HasPrefix(c.BaseURL, "http://")) {
		return nil, fmt.Errorf("the state version's download URL isn't a valid HTTPS URL")
	}
	body, status, err := c.get(ctx, version.DownloadURL, maxStateSize)
	if err != nil {
		return nil, fmt.Errorf("couldn't download the state: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("downloading the state returned %d", status)
	}
	return body, nil
}
