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
	// ApplyOn is "manual" to apply only when someone starts the pipeline, or "merge" to apply every
	// change merged to the default branch.
	ApplyOn string
	// GitState sets up the pipeline for the git state backend.
	GitState bool
	Force    bool
	Root     string // The repository root.

	// Cloud sign-in. Empty values leave a comment showing where to sign in instead.
	AWSPlanRole  string // The role that plans assume.
	AWSApplyRole string // The role that applies assume.
	AWSRegion    string
	// AzureClientID is the identity for both plans and applies, unless the plan or apply one is set.
	AzureClientID          string
	AzurePlanClientID      string
	AzureApplyClientID     string
	AzureTenantID          string
	AzureSubscriptionID    string
	AzureServiceConnection string // For Azure Pipelines, which signs in with a service connection.
}

// signinOptions is how one job, plan or apply, signs in to the cloud.
type signinOptions struct {
	Phase                                             string
	AWSRole, AWSRegion                                string
	AzureClientID, AzureTenantID, AzureSubscriptionID string
	AzureServiceConnection                            string
}

// AWS reports whether the job signs in to AWS.
func (s signinOptions) AWS() bool { return s.AWSRole != "" }

// Azure reports whether the job signs in to Azure.
func (s signinOptions) Azure() bool { return s.AzureClientID != "" || s.AzureServiceConnection != "" }

// Any reports whether the job signs in to any cloud.
func (s signinOptions) Any() bool { return s.AWS() || s.Azure() }

func (opts ScaffoldOptions) signin(phase string) signinOptions {
	s := signinOptions{
		Phase:                  phase,
		AWSRegion:              opts.AWSRegion,
		AzureClientID:          opts.AzureClientID,
		AzureTenantID:          opts.AzureTenantID,
		AzureSubscriptionID:    opts.AzureSubscriptionID,
		AzureServiceConnection: opts.AzureServiceConnection,
	}
	if phase == "plan" {
		s.AWSRole = opts.AWSPlanRole
		if opts.AzurePlanClientID != "" {
			s.AzureClientID = opts.AzurePlanClientID
		}
	} else {
		s.AWSRole = opts.AWSApplyRole
		if opts.AzureApplyClientID != "" {
			s.AzureClientID = opts.AzureApplyClientID
		}
	}
	return s
}

var (
	// releaseVersion matches the versions that have published releases.
	releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-(alpha|beta|rc)\d+)?$`)
	// safeValue limits what can be substituted into a generated YAML file.
	safeValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

	awsRoleARN = regexp.MustCompile(`^arn:aws[a-z-]*:iam::\d{12}:role/[A-Za-z0-9+=,.@_/-]+$`)
	awsRegion  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)
	azureGUID  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
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

// normalizeScaffoldOptions fills in defaults and checks every value that ends up in a generated file.
func normalizeScaffoldOptions(opts ScaffoldOptions) (ScaffoldOptions, error) {
	if _, ok := pipelineFiles[opts.Platform]; !ok {
		return opts, fmt.Errorf("unknown platform %q. Choose one of: %s, %s, %s, %s", opts.Platform, PlatformGitHub, PlatformGitLab, PlatformAzure, PlatformBitbucket)
	}
	if opts.DefaultBranch == "" {
		opts.DefaultBranch = "main"
	}
	if opts.Directory == "" {
		opts.Directory = "."
	}
	if opts.ApplyOn == "" {
		opts.ApplyOn = ApplyManual
	}
	if opts.ApplyOn != ApplyManual && opts.ApplyOn != ApplyMerge {
		return opts, fmt.Errorf("-apply-on must be %q or %q", ApplyManual, ApplyMerge)
	}
	for name, value := range map[string]string{"branch": opts.DefaultBranch, "directory": opts.Directory, "environment": opts.Environment} {
		if value != "." && !(name == "environment" && value == "") && !safeValue.MatchString(value) {
			return opts, fmt.Errorf("the %s %q contains unsupported characters", name, value)
		}
	}
	if err := validateSignin(&opts); err != nil {
		return opts, err
	}
	return opts, nil
}

func validateSignin(opts *ScaffoldOptions) error {
	for flag, value := range map[string]string{"-aws-plan-role": opts.AWSPlanRole, "-aws-apply-role": opts.AWSApplyRole} {
		if value != "" && !awsRoleARN.MatchString(value) {
			return fmt.Errorf("%s must be an IAM role ARN, like arn:aws:iam::123456789012:role/name", flag)
		}
	}
	if opts.AWSRegion == "" {
		opts.AWSRegion = "us-east-1"
	} else if !awsRegion.MatchString(opts.AWSRegion) {
		return fmt.Errorf("%q isn't an AWS region", opts.AWSRegion)
	}
	if opts.Platform == PlatformAzure && (opts.AWSPlanRole != "" || opts.AWSApplyRole != "") {
		return errors.New("Azure Pipelines can't sign in to AWS with OIDC without an extension, so -aws-plan-role and -aws-apply-role aren't supported for it. Use the AWS Toolkit's service connection instead")
	}

	usesClientID := false
	for flag, value := range map[string]string{
		"-azure-client-id": opts.AzureClientID, "-azure-plan-client-id": opts.AzurePlanClientID,
		"-azure-apply-client-id": opts.AzureApplyClientID,
	} {
		if value == "" {
			continue
		}
		usesClientID = true
		if !azureGUID.MatchString(value) {
			return fmt.Errorf("%s must be a client ID, like 00000000-0000-0000-0000-000000000000", flag)
		}
	}
	for flag, value := range map[string]string{"-azure-tenant-id": opts.AzureTenantID, "-azure-subscription-id": opts.AzureSubscriptionID} {
		if value != "" && !azureGUID.MatchString(value) {
			return fmt.Errorf("%s must be an ID, like 00000000-0000-0000-0000-000000000000", flag)
		}
	}
	if opts.AzureServiceConnection != "" {
		if opts.Platform != PlatformAzure {
			return errors.New("-azure-service-connection is only for Azure Pipelines. Use -azure-client-id on other platforms")
		}
		if !safeValue.MatchString(opts.AzureServiceConnection) {
			return fmt.Errorf("the service connection %q contains unsupported characters", opts.AzureServiceConnection)
		}
	}
	if usesClientID {
		if opts.Platform == PlatformAzure {
			return errors.New("Azure Pipelines signs in to Azure with a service connection. Use -azure-service-connection instead of client IDs")
		}
		if opts.AzureTenantID == "" || opts.AzureSubscriptionID == "" {
			return errors.New("signing in to Azure needs -azure-tenant-id and -azure-subscription-id as well as a client ID")
		}
	}
	return nil
}

// RenderPipeline returns the contents of a pipeline file.
func RenderPipeline(opts ScaffoldOptions) ([]byte, error) {
	opts, err := normalizeScaffoldOptions(opts)
	if err != nil {
		return nil, err
	}

	data := struct {
		DefaultBranch, Directory, Environment, ApplyOn string
		GitState                                       bool
		// ImageTag and ActionRef are for platforms that use the container image and the action.
		ImageTag, ActionRef, ReleaseVersion string
		// Plan and Apply say how each job signs in to the cloud.
		Plan, Apply signinOptions
	}{
		DefaultBranch:  opts.DefaultBranch,
		Directory:      opts.Directory,
		Environment:    opts.Environment,
		ApplyOn:        opts.ApplyOn,
		GitState:       opts.GitState,
		ImageTag:       "latest",
		ActionRef:      "main",
		ReleaseVersion: "latest",
		Plan:           opts.signin("plan"),
		Apply:          opts.signin("apply"),
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
	var parsed *template.Template
	funcs := template.FuncMap{
		// include renders a named template, so that its output can be piped to indent.
		"include": func(name string, data any) (string, error) {
			var out bytes.Buffer
			err := parsed.ExecuteTemplate(&out, name, data)
			return out.String(), err
		},
		"indent": func(spaces int, text string) string {
			lines := strings.Split(text, "\n")
			for i, line := range lines {
				if line != "" {
					lines[i] = strings.Repeat(" ", spaces) + line
				}
			}
			return strings.Join(lines, "\n")
		},
	}
	parsed, err = template.New(opts.Platform).Delims("[[", "]]").Option("missingkey=error").Funcs(funcs).Parse(string(source))
	if err != nil {
		return nil, err
	}
	// Each platform's sign-in steps are kept in one file, apart from the pipelines they slot into.
	signin, err := templateFiles.ReadFile("templates/signin.tmpl")
	if err != nil {
		return nil, err
	}
	if _, err := parsed.New("signin").Parse(string(signin)); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := parsed.Execute(&out, data); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// When a pipeline applies changes.
const (
	ApplyManual = "manual"
	ApplyMerge  = "merge"
)

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
func NextSteps(platform string, opts ScaffoldOptions) string {
	var steps []string
	if opts.GitState {
		steps = append(steps, "Configure the git backend with state encryption, and store its passphrase as a pipeline secret named BOGFORM_STATE_PASSPHRASE. Keep a copy somewhere safe: without it, the state can't be read.")
	} else {
		steps = append(steps, "Configure a remote state backend, such as s3. Pipelines start from a clean checkout, so local state is lost after each run.")
	}
	opts, _ = normalizeScaffoldOptions(opts)
	switch plan, apply := opts.signin("plan"), opts.signin("apply"); {
	case plan.Any() && apply.Any():
		// Both jobs sign in already.
	case plan.Any() || apply.Any():
		steps = append(steps, "Only one of the jobs signs in to your cloud account. Add sign-in to the other one, or rerun with both the plan and apply options.")
	default:
		steps = append(steps, "Give the pipeline access to your cloud account, using OIDC rather than stored keys. The file has a comment showing where. \"bogform bootstrap aws\" or \"bogform bootstrap azure\" can set up the roles, and \"bogform ci init\" can fill them in with options like -aws-plan-role and -aws-apply-role.")
	}
	switch platform {
	case PlatformGitHub:
		if opts.Environment != "" {
			steps = append(steps, "If your plan supports it, add required reviewers to the \""+opts.Environment+"\" environment to approve each apply. Private repositories need a paid plan for this; use -environment= to leave it out.")
		}
	case PlatformGitLab:
		steps = append(steps,
			"Add a project access token with the api scope as a masked variable named BOG_CI_TOKEN, so plans can comment on merge requests.",
			"Protect the deployment environment to require approval for each apply.")
	case PlatformAzure:
		steps = append(steps,
			"Add this file as a pipeline, and add a build validation policy to your default branch so it runs on pull requests.",
			"Add an approval check to the deployment environment.")
	case PlatformBitbucket:
		steps = append(steps, "Add an access token with pull request write access as a secured variable named BOG_CI_TOKEN, so plans can comment.")
	}

	var out strings.Builder
	out.WriteString("\nNext steps:\n")
	for i, step := range steps {
		fmt.Fprintf(&out, "  %d. %s\n", i+1, step)
	}
	if opts.ApplyOn == ApplyMerge {
		out.WriteString("\nChanges are applied when they're merged to the default branch.\n")
	} else {
		out.WriteString("\nPull requests get a plan. To apply, run the pipeline manually on the default branch.\n")
	}
	return out.String()
}
