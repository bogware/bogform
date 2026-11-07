// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package bootstrap writes the configuration that gives repository pipelines access to a cloud
// account: an OIDC trust, and for each repository a read-only identity for plans and an identity
// for applies that only the default branch can use.
package bootstrap

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

//go:embed templates/*/*.tmpl
var templateFiles embed.FS

// Platforms whose OIDC tokens the generated configuration trusts.
const (
	PlatformGitHub = "github"
	PlatformGitLab = "gitlab"
)

// Repository is one repository whose pipeline gets a plan and an apply identity.
type Repository struct {
	// Key names the identities, as <Key>-ci-plan and <Key>-ci-apply.
	Key string
	// Name is the repository's path, like owner/name.
	Name string
	// SubjectPrefix is the start of the "sub" claim in the repository's OIDC tokens.
	SubjectPrefix string
}

var (
	repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	subjectPrefix  = regexp.MustCompile(`^[A-Za-z0-9_.@:/-]+$`)
	branchName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	keyUnsafe      = regexp.MustCompile(`[^a-z0-9-]+`)
)

// ParseRepository reads a repository given as owner/name, or owner/name=subject-prefix.
func ParseRepository(value, platform string) (Repository, error) {
	name, prefix, _ := strings.Cut(strings.TrimSpace(value), "=")
	if !repositoryName.MatchString(name) {
		return Repository{}, fmt.Errorf("%q isn't a repository. Use owner/name", name)
	}
	if prefix == "" {
		if platform == PlatformGitLab {
			prefix = "project_path:" + name
		} else {
			prefix = "repo:" + name
		}
	} else if !subjectPrefix.MatchString(prefix) {
		return Repository{}, fmt.Errorf("%q isn't a valid subject prefix", prefix)
	}
	parts := strings.Split(name, "/")
	key := strings.Trim(keyUnsafe.ReplaceAllString(strings.ToLower(parts[len(parts)-1]), "-"), "-")
	if key == "" {
		return Repository{}, fmt.Errorf("can't make a name for identities from %q", name)
	}
	return Repository{Key: key, Name: name, SubjectPrefix: prefix}, nil
}

// HCLKey is the repository's key as an HCL identifier, for naming blocks.
func (r Repository) HCLKey() string { return strings.ReplaceAll(r.Key, "-", "_") }

// File is one generated file.
type File struct {
	Name    string
	Content []byte
}

// AWSOptions configures the AWS configuration.
type AWSOptions struct {
	Platform  string
	GitLabURL string // For GitLab: the instance's URL. Defaults to https://gitlab.com.
	Region    string
	Branch    string // The branch that applies run from.
	// Repositories get plan and apply roles that can only do what their apply policies say.
	Repositories []Repository
	// Admin is the repository that holds this configuration. Its apply role can manage the OIDC
	// provider and the other repositories' roles. Nil leaves that to a workstation.
	Admin *Repository
	// PassphraseParameter is where the state passphrase's backup is kept in SSM Parameter Store.
	PassphraseParameter string
}

// AzureOptions configures the Azure configuration.
type AzureOptions struct {
	Branch       string
	Location     string
	Repositories []Repository
	// IdentityResourceGroup is created to hold the identities.
	IdentityResourceGroup string
	// ResourceGroup limits what the identities can see and change. Empty means the whole subscription.
	ResourceGroup string
	// PassphraseVaultSecret is a Key Vault secret URL holding a backup of the state passphrase.
	PassphraseVaultSecret string
}

var (
	awsRegion     = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d+$`)
	ssmName       = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	azureLocation = regexp.MustCompile(`^[a-z0-9]+$`)
	azureRGName   = regexp.MustCompile(`^[A-Za-z0-9_().-]{1,90}$`)
	vaultSecret   = regexp.MustCompile(`^https://[a-z0-9-]+\.vault\.azure\.net/secrets/[A-Za-z0-9-]+$`)
)

func checkRepositories(repositories []Repository, admin *Repository) error {
	if len(repositories) == 0 && admin == nil {
		return errors.New("name at least one repository with -repository")
	}
	seen := map[string]string{}
	all := repositories
	if admin != nil {
		all = append([]Repository{*admin}, repositories...)
	}
	for _, r := range all {
		if other, ok := seen[r.Key]; ok && other != r.Name {
			return fmt.Errorf("%s and %s would both have identities named %q", other, r.Name, r.Key)
		}
		seen[r.Key] = r.Name
	}
	return nil
}

// AWS returns the files for an AWS account.
func AWS(opts AWSOptions) ([]File, error) {
	if opts.Platform == "" {
		opts.Platform = PlatformGitHub
	}
	if opts.Platform != PlatformGitHub && opts.Platform != PlatformGitLab {
		return nil, fmt.Errorf("-platform must be %q or %q", PlatformGitHub, PlatformGitLab)
	}
	if opts.Region == "" {
		opts.Region = "us-east-1"
	}
	if !awsRegion.MatchString(opts.Region) {
		return nil, fmt.Errorf("%q isn't an AWS region", opts.Region)
	}
	if opts.Branch == "" {
		opts.Branch = "main"
	}
	if !branchName.MatchString(opts.Branch) {
		return nil, fmt.Errorf("%q isn't a valid branch", opts.Branch)
	}
	if opts.GitLabURL == "" {
		opts.GitLabURL = "https://gitlab.com"
	}
	gitlab, err := url.Parse(opts.GitLabURL)
	if err != nil || gitlab.Scheme != "https" || gitlab.Host == "" || gitlab.Path != "" && gitlab.Path != "/" {
		return nil, fmt.Errorf("%q isn't a GitLab URL, like https://gitlab.com", opts.GitLabURL)
	}
	if opts.PassphraseParameter != "" && !ssmName.MatchString(opts.PassphraseParameter) {
		return nil, fmt.Errorf("%q isn't an SSM parameter name, like /team/state-passphrase", opts.PassphraseParameter)
	}
	if err := checkRepositories(opts.Repositories, opts.Admin); err != nil {
		return nil, err
	}

	issuerHost := "token.actions.githubusercontent.com"
	audience := "sts.amazonaws.com"
	if opts.Platform == PlatformGitLab {
		issuerHost = gitlab.Host
		audience = "https://" + gitlab.Host
	}
	data := map[string]any{
		"Options":    opts,
		"GitHub":     opts.Platform == PlatformGitHub,
		"IssuerHost": issuerHost,
		"Audience":   audience,
		"All":        allRepositories(opts.Repositories, opts.Admin),
		"Keychain":   keychainName(opts.Repositories, opts.Admin),
	}
	return render("aws", data)
}

// Azure returns the files for an Azure subscription.
func Azure(opts AzureOptions) ([]File, error) {
	if opts.Branch == "" {
		opts.Branch = "main"
	}
	if !branchName.MatchString(opts.Branch) {
		return nil, fmt.Errorf("%q isn't a valid branch", opts.Branch)
	}
	if opts.Location == "" {
		opts.Location = "eastus"
	}
	if !azureLocation.MatchString(opts.Location) {
		return nil, fmt.Errorf("%q isn't an Azure location, like eastus", opts.Location)
	}
	if opts.IdentityResourceGroup == "" {
		opts.IdentityResourceGroup = "bogform-ci"
	}
	for _, rg := range []string{opts.IdentityResourceGroup, opts.ResourceGroup} {
		if rg != "" && !azureRGName.MatchString(rg) {
			return nil, fmt.Errorf("%q isn't a valid resource group name", rg)
		}
	}
	if opts.PassphraseVaultSecret != "" && !vaultSecret.MatchString(opts.PassphraseVaultSecret) {
		return nil, fmt.Errorf("%q isn't a Key Vault secret URL, like https://my-vault.vault.azure.net/secrets/state-passphrase", opts.PassphraseVaultSecret)
	}
	if err := checkRepositories(opts.Repositories, nil); err != nil {
		return nil, err
	}
	data := map[string]any{
		"Options":  opts,
		"All":      opts.Repositories,
		"Keychain": keychainName(opts.Repositories, nil),
	}
	return render("azure", data)
}

func allRepositories(repositories []Repository, admin *Repository) []Repository {
	if admin == nil {
		return repositories
	}
	all := []Repository{*admin}
	for _, r := range repositories {
		if r.Key != admin.Key {
			all = append(all, r)
		}
	}
	return all
}

// keychainName is the keychain entry that holds the bootstrap configuration's own state passphrase.
func keychainName(repositories []Repository, admin *Repository) string {
	if admin != nil {
		return admin.Key
	}
	return "bogform-bootstrap"
}

func render(cloud string, data map[string]any) ([]File, error) {
	entries, err := templateFiles.ReadDir("templates/" + cloud)
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		// quote writes an HCL string. Every substituted value is validated, so this only guards
		// against values that need escaping.
		"quote": func(s string) string {
			s = strings.ReplaceAll(s, `\`, `\\`)
			s = strings.ReplaceAll(s, `"`, `\"`)
			s = strings.ReplaceAll(s, "${", "$${")
			s = strings.ReplaceAll(s, "%{", "%%{")
			return `"` + s + `"`
		},
	}
	var files []File
	for _, entry := range entries {
		source, err := templateFiles.ReadFile("templates/" + cloud + "/" + entry.Name())
		if err != nil {
			return nil, err
		}
		parsed, err := template.New(entry.Name()).Option("missingkey=error").Funcs(funcs).Parse(string(source))
		if err != nil {
			return nil, err
		}
		var out bytes.Buffer
		if err := parsed.Execute(&out, data); err != nil {
			return nil, fmt.Errorf("rendering %s: %w", entry.Name(), err)
		}
		files = append(files, File{
			Name:    strings.TrimSuffix(entry.Name(), ".tmpl"),
			Content: hclwrite.Format(collapseBlankLines(out.Bytes())),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

var blankLines = regexp.MustCompile(`\n{3,}`)

// collapseBlankLines tidies the gaps that template conditionals leave behind.
func collapseBlankLines(content []byte) []byte {
	content = blankLines.ReplaceAll(content, []byte("\n\n"))
	return append(bytes.TrimSpace(content), '\n')
}

// Write saves files into dir. It refuses to replace existing files unless force is set.
func Write(dir string, files []File, force bool) error {
	if !force {
		for _, f := range files {
			path := filepath.Join(dir, f.Name)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists. Use -force to replace it, or choose another directory with -out", path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
