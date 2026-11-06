// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package server implements the optional self-hosted Bogform dashboard. It records what pipelines did
// and can start a pipeline run. It doesn't run Bogform or hold cloud credentials.
package server

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// PlaceholderSecret is the value that the deployment module gives new secrets, for an administrator to
// replace. It's refused here so that a forgotten step can't become a known key.
const PlaceholderSecret = "change-me"

// Config is the server's configuration. See LoadConfig for how each value is set.
type Config struct {
	PublicURL   string
	DatabaseURL string
	SessionKey  []byte
	// OriginSecret, if set, must be on every request. CloudFront adds it, so that requests can't
	// bypass CloudFront by going straight to the function's URL.
	OriginSecret  string
	StaticDir     string
	AutoMigrate   bool
	RetentionDays int

	Auth AuthConfig

	// Who can sign in. Each entry is an email address, or a domain such as "@example.com".
	Viewers   []string
	Operators []string

	// DevUser signs everyone in as this email address. It's only allowed on a loopback address.
	DevUser string

	Triggers TriggerConfig
}

// AuthConfig selects how people sign in.
type AuthConfig struct {
	// Provider is "oidc" or "github".
	Provider     string
	Issuer       string
	ClientID     string
	ClientSecret string
	// TrustEmail accepts an email claim that the provider doesn't mark as verified. Only use it with a
	// single-organization provider that controls its users' addresses.
	TrustEmail bool
	// GitHubURL and GitHubAPIURL are for GitHub Enterprise Server.
	GitHubURL    string
	GitHubAPIURL string
}

// TriggerConfig holds what the server needs to start pipeline runs.
type TriggerConfig struct {
	// Ref is the branch that pipelines are started on. It's configured here rather than learned from
	// reports, because anyone who can send a report could otherwise choose what an operator's click runs.
	Ref            string
	GitHubToken    string
	GitHubAPIURL   string
	GitHubWorkflow string
	GitLabToken    string
	GitLabAPIURL   string
	BitbucketToken string
	BitbucketURL   string
	AzureToken     string
	AzureOrgURL    string
	// AzurePipelines maps a repository name to "project/pipelineID".
	AzurePipelines map[string]string
}

// LoadConfig reads the configuration from BOG_SERVER_* environment variables.
func LoadConfig(env func(string) string) (Config, error) {
	get := func(name string) string { return strings.TrimSpace(env("BOG_SERVER_" + name)) }
	orDefault := func(name, fallback string) string {
		if value := get(name); value != "" {
			return value
		}
		return fallback
	}
	boolean := func(name string, fallback bool) (bool, error) {
		value := get(name)
		if value == "" {
			return fallback, nil
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("BOG_SERVER_%s must be true or false", name)
		}
		return parsed, nil
	}

	cfg := Config{
		PublicURL:    strings.TrimSuffix(get("PUBLIC_URL"), "/"),
		DatabaseURL:  get("DATABASE_URL"),
		OriginSecret: get("ORIGIN_SECRET"),
		StaticDir:    get("STATIC_DIR"),
		DevUser:      get("DEV_USER"),
		Viewers:      splitList(get("VIEWERS")),
		Operators:    splitList(get("OPERATORS")),
		Auth: AuthConfig{
			Provider:     orDefault("AUTH_PROVIDER", "oidc"),
			Issuer:       strings.TrimSuffix(get("OIDC_ISSUER"), "/"),
			ClientID:     get("OIDC_CLIENT_ID"),
			ClientSecret: get("OIDC_CLIENT_SECRET"),
			GitHubURL:    strings.TrimSuffix(orDefault("GITHUB_URL", "https://github.com"), "/"),
			GitHubAPIURL: strings.TrimSuffix(orDefault("GITHUB_API_URL", "https://api.github.com"), "/"),
		},
		Triggers: TriggerConfig{
			Ref:            orDefault("TRIGGER_REF", "main"),
			GitHubToken:    get("GITHUB_TOKEN"),
			GitHubAPIURL:   strings.TrimSuffix(orDefault("GITHUB_API_URL", "https://api.github.com"), "/"),
			GitHubWorkflow: orDefault("GITHUB_WORKFLOW", "bogform.yml"),
			GitLabToken:    get("GITLAB_TOKEN"),
			GitLabAPIURL:   strings.TrimSuffix(orDefault("GITLAB_API_URL", "https://gitlab.com/api/v4"), "/"),
			BitbucketToken: get("BITBUCKET_TOKEN"),
			BitbucketURL:   strings.TrimSuffix(orDefault("BITBUCKET_API_URL", "https://api.bitbucket.org/2.0"), "/"),
			AzureToken:     get("AZURE_TOKEN"),
			AzureOrgURL:    strings.TrimSuffix(get("AZURE_ORG_URL"), "/"),
			AzurePipelines: map[string]string{},
		},
	}

	var err error
	if cfg.AutoMigrate, err = boolean("AUTO_MIGRATE", true); err != nil {
		return cfg, err
	}
	if cfg.Auth.TrustEmail, err = boolean("OIDC_TRUST_EMAIL", false); err != nil {
		return cfg, err
	}
	cfg.RetentionDays = 365
	if value := get("RETENTION_DAYS"); value != "" {
		if cfg.RetentionDays, err = strconv.Atoi(value); err != nil || cfg.RetentionDays < 0 {
			return cfg, errors.New("BOG_SERVER_RETENTION_DAYS must be a whole number, or 0 to keep runs forever")
		}
	}
	for _, entry := range strings.FieldsFunc(get("AZURE_PIPELINES"), func(r rune) bool { return r == ',' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		repository, pipeline, found := strings.Cut(entry, "=")
		if !found {
			return cfg, fmt.Errorf("BOG_SERVER_AZURE_PIPELINES entry %q must look like repository=project/123", entry)
		}
		cfg.Triggers.AzurePipelines[repository] = pipeline
	}
	if value := get("SESSION_KEY"); value != "" && value != PlaceholderSecret {
		if cfg.SessionKey, err = hex.DecodeString(value); err != nil {
			return cfg, errors.New("BOG_SERVER_SESSION_KEY must be hexadecimal. Generate one with: openssl rand -hex 32")
		}
	}
	return cfg, cfg.Validate()
}

// Validate reports problems that would stop the server from working safely.
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	public, err := url.Parse(c.PublicURL)
	loopback := false
	if err != nil || public.Host == "" || public.Path != "" {
		add("BOG_SERVER_PUBLIC_URL must be the address people use to reach the server, such as https://bogform.example.com")
	} else {
		loopback = isLoopback(public.Hostname())
		if public.Scheme != "https" && !loopback {
			add("BOG_SERVER_PUBLIC_URL must use https, because it carries sign-in cookies")
		}
	}
	if c.DatabaseURL == "" || c.DatabaseURL == PlaceholderSecret {
		add("BOG_SERVER_DATABASE_URL must be a Postgres connection string")
	}
	if len(c.SessionKey) < 32 {
		add("BOG_SERVER_SESSION_KEY must be at least 32 bytes of hexadecimal. Generate one with: openssl rand -hex 32")
	}
	if c.DevUser != "" {
		if !loopback {
			add("BOG_SERVER_DEV_USER skips sign-in, so it only works when BOG_SERVER_PUBLIC_URL is a loopback address")
		}
	} else {
		if len(c.Viewers)+len(c.Operators) == 0 {
			add("nobody could sign in. Set BOG_SERVER_VIEWERS or BOG_SERVER_OPERATORS to a list of email addresses or domains, such as @example.com")
		}
		switch c.Auth.Provider {
		case "oidc":
			if c.Auth.Issuer == "" {
				add("BOG_SERVER_OIDC_ISSUER is required for OIDC sign-in")
			}
		case "github":
		default:
			add("BOG_SERVER_AUTH_PROVIDER must be oidc or github")
		}
		if c.Auth.ClientID == "" || c.Auth.ClientSecret == "" || c.Auth.ClientSecret == PlaceholderSecret {
			add("BOG_SERVER_OIDC_CLIENT_ID and BOG_SERVER_OIDC_CLIENT_SECRET are required for sign-in")
		}
	}
	if !validRef.MatchString(c.Triggers.Ref) {
		add("BOG_SERVER_TRIGGER_REF isn't a valid branch name")
	}
	if c.OriginSecret == PlaceholderSecret {
		add("BOG_SERVER_ORIGIN_SECRET still has its placeholder value")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}

// validRef matches branch names that are safe to send to a pipeline platform.
var validRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// IsLoopback reports whether host refers to this machine.
func IsLoopback(host string) bool { return isLoopback(host) }

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		out = append(out, strings.ToLower(item))
	}
	return out
}

// Roles a person can have.
const (
	RoleNone     = ""
	RoleViewer   = "viewer"
	RoleOperator = "operator"
)

// RoleFor returns a person's role. It's checked on every request, so removing someone from the
// configuration takes effect immediately, even if they have a session.
func (c Config) RoleFor(email string) string {
	email = strings.ToLower(email)
	matches := func(list []string) bool {
		_, domain, _ := strings.Cut(email, "@")
		for _, entry := range list {
			if entry == email || (strings.HasPrefix(entry, "@") && domain != "" && entry == "@"+domain) {
				return true
			}
		}
		return false
	}
	switch {
	case c.DevUser != "" && strings.EqualFold(email, c.DevUser):
		return RoleOperator
	case matches(c.Operators):
		return RoleOperator
	case matches(c.Viewers):
		return RoleViewer
	}
	return RoleNone
}
