// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

//go:embed templates/*.tmpl
var templateFiles embed.FS

// pipelineFiles maps each platform to where its pipeline file belongs in a repository.
var pipelineFiles = map[string]string{
	PlatformGitHub:    ".github/workflows/bogform.yml",
	PlatformGitLab:    ".gitlab-ci.yml",
	PlatformAzure:     "azure-pipelines.yml",
	PlatformBitbucket: "bitbucket-pipelines.yml",
}

// ScaffoldOptions configures a generated pipeline file.
type ScaffoldOptions struct {
	Platform      string
	Version       string // The version of Bogform to pin, or empty to use the latest release.
	DefaultBranch string
	Directory     string
	Environment   string
	Force         bool
	Root          string // The repository root.
}

var (
	// releaseVersion matches the versions that have published releases.
	releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-(alpha|beta|rc)\d+)?$`)
	// safeValue limits what can be substituted into a generated YAML file.
	safeValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

// Scaffold writes a pipeline file and returns its path.
func Scaffold(opts ScaffoldOptions) (string, error) {
	content, err := RenderPipeline(opts)
	if err != nil {
		return "", err
	}
	relative := pipelineFiles[opts.Platform]
	path := filepath.Join(opts.Root, filepath.FromSlash(relative))
	if !opts.Force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("%s already exists. Use -force to replace it, or copy what you need from a new file", relative)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return relative, os.WriteFile(path, content, 0o644)
}

// RenderPipeline returns the contents of a pipeline file.
func RenderPipeline(opts ScaffoldOptions) ([]byte, error) {
	if _, ok := pipelineFiles[opts.Platform]; !ok {
		return nil, fmt.Errorf("unknown platform %q. Choose one of: %s, %s, %s, %s", opts.Platform, PlatformGitHub, PlatformGitLab, PlatformAzure, PlatformBitbucket)
	}
	if opts.DefaultBranch == "" {
		opts.DefaultBranch = "main"
	}
	if opts.Directory == "" {
		opts.Directory = "."
	}
	if opts.Environment == "" {
		opts.Environment = "production"
	}
	for name, value := range map[string]string{"branch": opts.DefaultBranch, "directory": opts.Directory, "environment": opts.Environment} {
		if value != "." && !safeValue.MatchString(value) {
			return nil, fmt.Errorf("the %s %q contains unsupported characters", name, value)
		}
	}

	data := struct {
		DefaultBranch, Directory, Environment string
		// ImageTag and ActionRef are for platforms that use the container image and the action.
		ImageTag, ActionRef, ReleaseVersion string
	}{
		DefaultBranch:  opts.DefaultBranch,
		Directory:      opts.Directory,
		Environment:    opts.Environment,
		ImageTag:       "latest",
		ActionRef:      "main",
		ReleaseVersion: "latest",
	}
	if version := strings.TrimPrefix(opts.Version, "v"); releaseVersion.MatchString(version) {
		data.ImageTag = version
		data.ActionRef = "v" + version
		data.ReleaseVersion = version
	}

	source, err := templateFiles.ReadFile("templates/" + opts.Platform + ".yml.tmpl")
	if err != nil {
		return nil, err
	}
	// Pipeline syntax uses {{ }} itself, so these templates use different delimiters.
	parsed, err := template.New(opts.Platform).Delims("[[", "]]").Option("missingkey=error").Parse(string(source))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// DetectPlatformFromRemote guesses the platform from a Git remote URL, or returns an empty string.
func DetectPlatformFromRemote(remote string) string {
	remote = strings.ToLower(remote)
	switch {
	case strings.Contains(remote, "github."):
		return PlatformGitHub
	case strings.Contains(remote, "gitlab."):
		return PlatformGitLab
	case strings.Contains(remote, "dev.azure.com"), strings.Contains(remote, "visualstudio.com"):
		return PlatformAzure
	case strings.Contains(remote, "bitbucket."):
		return PlatformBitbucket
	}
	return ""
}

// NextSteps explains what to do after generating a pipeline file for a platform.
func NextSteps(platform string) string {
	common := "\nNext steps:\n" +
		"  1. Configure a remote state backend, such as s3. Pipelines start from a clean checkout, so local state is lost after each run.\n" +
		"  2. Give the pipeline access to your cloud account, using OIDC rather than stored keys. The file has a comment showing where.\n"
	switch platform {
	case PlatformGitHub:
		return common + "  3. Create a \"production\" environment with required reviewers to approve each apply.\n"
	case PlatformGitLab:
		return common + "  3. Add a project access token with the api scope as a masked variable named BOG_CI_TOKEN, so plans can comment on merge requests.\n" +
			"  4. Protect the \"production\" environment to require approval for each apply.\n"
	case PlatformAzure:
		return common + "  3. Add this file as a pipeline, and add a build validation policy to your default branch so it runs on pull requests.\n" +
			"  4. Add an approval check to the \"production\" environment.\n"
	case PlatformBitbucket:
		return common + "  3. Add an access token with pull request write access as a secured variable named BOG_CI_TOKEN, so plans can comment.\n" +
			"  4. Require approval on the \"production\" deployment environment, if your plan includes it.\n"
	}
	return common
}
