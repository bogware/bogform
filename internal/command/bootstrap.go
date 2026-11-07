// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bogware/bogform/internal/bootstrap"
)

// BootstrapCommander builds the "bootstrap" command, which writes the configuration that gives
// repository pipelines access to a cloud account.
func BootstrapCommander() Command {
	return Command{
		Name:  "bootstrap",
		Short: "Set up pipeline access to a cloud account",
		Long: `Writes a configuration that lets repository pipelines reach a cloud account without stored keys. Each repository gets a read-only identity for plans, and an identity for applies that only its default branch can use.

The configuration is yours to review and edit. Apply it once from a workstation, then pass the identities to "bogform ci init" in each repository.`,
		Commands: []Command{bootstrapAWSCommander(), bootstrapAzureCommander()},
	}
}

func bootstrapAWSCommander() Command {
	cmd := Command{
		Name:  "aws",
		Short: "Set up pipeline access to an AWS account",
		Long: `Writes a configuration with an OIDC provider for GitHub Actions or GitLab, and for each repository a <name>-ci-plan role and a <name>-ci-apply role.

Plan roles are read-only, and can't read data such as S3 objects, secrets or logs. Apply roles start with a placeholder policy that allows nothing useful: replace it with what each repository manages.

The repository that holds the configuration (-admin-repository, which defaults to this repository's GitHub remote) gets an apply role that can manage the provider and the other roles, so its pipeline can manage the configuration after the first apply.`,
		DiagsWithNewline: true,
	}
	var repositories []string
	var platform, gitlabURL, region, branch, admin, parameter, subjectPrefix, out string
	var force bool
	cmd.CommandLine.StringArrayVar(&repositories, "repository", nil, "A repository whose pipeline needs access, as owner/name. Add =PREFIX for a GitHub repository that uses immutable OIDC subjects, like owner/name=repo:owner@123/name@456. Can be used more than once.")
	cmd.CommandLine.StringVar(&admin, "admin-repository", "", "The repository that will hold this configuration, as owner/name[=PREFIX]. Defaults to this repository, if its origin is on GitHub. Use -admin-repository=none to manage the configuration only from a workstation.")
	cmd.CommandLine.StringVar(&subjectPrefix, "subject-prefix", "", "The OIDC subject prefix for the only -repository, as an alternative to owner/name=PREFIX.")
	cmd.CommandLine.StringVar(&platform, "platform", bootstrap.PlatformGitHub, "Where pipelines run: github or gitlab.")
	cmd.CommandLine.StringVar(&gitlabURL, "gitlab-url", "https://gitlab.com", "For GitLab: the URL of your GitLab instance.")
	cmd.CommandLine.StringVar(&region, "region", "us-east-1", "The AWS region.")
	cmd.CommandLine.StringVar(&branch, "branch", "main", "The branch that applies run from.")
	cmd.CommandLine.StringVar(&parameter, "passphrase-parameter", "", "An SSM parameter that holds a backup of the state passphrase, like /team/state-passphrase. Workstations fall back to it.")
	cmd.CommandLine.StringVar(&out, "out", "bootstrap", "The directory to write the configuration to.")
	cmd.CommandLine.BoolVar(&force, "force", false, "Replace files that already exist.")

	cmd.Run = func(Meta) int {
		repos, err := parseRepositories(repositories, subjectPrefix, platform)
		if err != nil {
			return bootstrapFailed(err)
		}
		opts := bootstrap.AWSOptions{
			Platform: platform, GitLabURL: gitlabURL, Region: region, Branch: branch,
			Repositories: repos, PassphraseParameter: parameter,
		}
		if admin == "" && platform == bootstrap.PlatformGitHub {
			admin = originRepository()
		}
		if admin != "" && admin != "none" {
			r, err := bootstrap.ParseRepository(admin, platform)
			if err != nil {
				return bootstrapFailed(err)
			}
			opts.Admin = &r
		}
		files, err := bootstrap.AWS(opts)
		if err != nil {
			return bootstrapFailed(err)
		}
		if err := bootstrap.Write(out, files, force); err != nil {
			return bootstrapFailed(err)
		}
		printWritten(out, files)
		fmt.Print(`
Next steps:
  1. Replace the placeholder apply policy for each repository in ci_roles.tf with what it manages.
  2. Check each repository's subject_prefix. GitHub repositories created recently use immutable
     subjects, and their roles can't be assumed until the prefix matches.
  3. Store a state passphrase: "bogform state passphrase set <name>" saves it in your keychain.
     Keep a backup, because without it the state can't be read.
  4. Apply it once from a workstation with administrator access:
       bogform -chdir=` + out + ` init
       bogform -chdir=` + out + ` apply
  5. In each repository, run "bogform ci init" with the role ARNs from the ci_roles output:
       bogform ci init -aws-plan-role <plan> -aws-apply-role <apply>
`)
		return 0
	}
	return cmd
}

func bootstrapAzureCommander() Command {
	cmd := Command{
		Name:  "azure",
		Short: "Set up pipeline access to an Azure subscription",
		Long: `Writes a configuration with two user-assigned managed identities for each GitHub repository, trusted through OIDC federated credentials: <name>-ci-plan, with the Reader role, for pull requests, and <name>-ci-apply, with the Contributor role, for the default branch.

Neither role can create role assignments, so this configuration is always applied from a workstation, by someone who can.`,
		DiagsWithNewline: true,
	}
	var repositories []string
	var branch, location, identityGroup, resourceGroup, vaultSecret, subjectPrefix, out string
	var force bool
	cmd.CommandLine.StringArrayVar(&repositories, "repository", nil, "A GitHub repository whose pipeline needs access, as owner/name. Add =PREFIX for a repository that uses immutable OIDC subjects. Can be used more than once.")
	cmd.CommandLine.StringVar(&subjectPrefix, "subject-prefix", "", "The OIDC subject prefix for the only -repository, as an alternative to owner/name=PREFIX.")
	cmd.CommandLine.StringVar(&resourceGroup, "resource-group", "", "Limit the identities to an existing resource group. Without this, they can see the whole subscription, and applies can change all of it.")
	cmd.CommandLine.StringVar(&identityGroup, "identity-resource-group", "bogform-ci", "The resource group to create for the identities.")
	cmd.CommandLine.StringVar(&location, "location", "eastus", "The Azure location for the identities.")
	cmd.CommandLine.StringVar(&branch, "branch", "main", "The branch that applies run from.")
	cmd.CommandLine.StringVar(&vaultSecret, "passphrase-key-vault-secret", "", "A Key Vault secret URL that holds a backup of the state passphrase. Workstations fall back to it.")
	cmd.CommandLine.StringVar(&out, "out", "bootstrap", "The directory to write the configuration to.")
	cmd.CommandLine.BoolVar(&force, "force", false, "Replace files that already exist.")

	cmd.Run = func(Meta) int {
		repos, err := parseRepositories(repositories, subjectPrefix, bootstrap.PlatformGitHub)
		if err != nil {
			return bootstrapFailed(err)
		}
		files, err := bootstrap.Azure(bootstrap.AzureOptions{
			Branch: branch, Location: location, Repositories: repos,
			IdentityResourceGroup: identityGroup, ResourceGroup: resourceGroup, PassphraseVaultSecret: vaultSecret,
		})
		if err != nil {
			return bootstrapFailed(err)
		}
		if err := bootstrap.Write(out, files, force); err != nil {
			return bootstrapFailed(err)
		}
		printWritten(out, files)
		fmt.Print(`
Next steps:
  1. Check each repository's subject_prefix. GitHub repositories created recently use immutable
     subjects, and their identities can't be used until the prefix matches.
  2. Store a state passphrase: "bogform state passphrase set <name>" saves it in your keychain.
     Keep a backup, because without it the state can't be read.
  3. Apply it from a workstation signed in with "az login" as an Owner of the subscription:
       bogform -chdir=` + out + ` init
       bogform -chdir=` + out + ` apply -var subscription_id=<subscription>
  4. In each repository, run the "bogform ci init" command from the ci_identities output.
`)
		return 0
	}
	return cmd
}

func parseRepositories(values []string, subjectPrefix, platform string) ([]bootstrap.Repository, error) {
	if subjectPrefix != "" {
		if len(values) != 1 || strings.Contains(values[0], "=") {
			return nil, fmt.Errorf("-subject-prefix needs exactly one -repository, without its own =PREFIX")
		}
		values = []string{values[0] + "=" + subjectPrefix}
	}
	var repos []bootstrap.Repository
	for _, value := range values {
		r, err := bootstrap.ParseRepository(value, platform)
		if err != nil {
			return nil, err
		}
		repos = append(repos, r)
	}
	return repos, nil
}

var githubRemote = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(\.git)?/?$`)

// originRepository returns owner/name for this repository's origin, if it's on GitHub.
func originRepository() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	if m := githubRemote.FindStringSubmatch(strings.TrimSpace(string(out))); m != nil {
		return m[1]
	}
	return ""
}

func printWritten(dir string, files []bootstrap.File) {
	for _, f := range files {
		fmt.Printf("Wrote %s\n", filepath.Join(dir, f.Name))
	}
}

func bootstrapFailed(err error) int {
	fmt.Fprintf(os.Stderr, "Error: %s\n", err)
	return 1
}
