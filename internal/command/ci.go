// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bogware/bogform/internal/ci"
	"github.com/bogware/bogform/version"
)

// CICommander builds the "ci" command, which runs Bogform from repository pipelines.
func CICommander() Command {
	return Command{
		Name:  "ci",
		Short: "Run Bogform from a repository pipeline",
		Long: `These commands run Bogform in GitHub Actions, GitLab CI, Azure Pipelines, or Bitbucket Pipelines.

They initialize the working directory, run the operation, publish a report, and comment on the current pull request. Use "bogform ci init" to add a pipeline to your repository.`,
		Commands: []Command{
			ciRunCommander("plan", "Plan changes and comment on the pull request",
				"Initializes the working directory, creates a plan, and comments on the current pull request with the result. A plan with changes is still a success.\n\nThe comment lists anything the plan destroys or replaces, and warnings from built-in checks, such as for public storage or administration ports open to the internet. With -cost, it also estimates the monthly cost.",
				(*ci.Runner).Plan),
			ciRunCommander("apply", "Plan and apply changes",
				"Initializes the working directory, creates a plan, and applies it without asking for approval. Use your platform's environment approvals as the gate.\n\nThis command refuses to run for a pull request unless you use -allow-pull-request-apply, because the code on a pull request might not be reviewed.\n\nIt also refuses a plan that destroys or replaces resources, unless you use -allow-destroy, set BOG_CI_ALLOW_DESTROY=true, or on GitHub, the merged pull request has the allow-destroy label.",
				(*ci.Runner).Apply),
			ciRunCommander("drift", "Check whether real infrastructure has drifted",
				"Runs a refresh-only plan to compare real infrastructure with the state. Exits with 0 if nothing has drifted, 1 on an error, and 2 if there's drift.",
				(*ci.Runner).Drift),
			ciInitCommander(),
			ciContextCommander(),
		},
	}
}

func ciRunCommander(name, short, long string, run func(*ci.Runner, context.Context) int) Command {
	cmd := Command{Name: name, Short: short, Long: long, DiagsWithNewline: true}

	var workspace string
	var vars, varFiles, args []string
	var allowPR, noComment, noOutput, allowDestroy, cost bool
	checks := true
	var skipChecks string
	cmd.CommandLine.StringVar(&workspace, "workspace", "", "The workspace to use, created if it doesn't exist. Defaults to BOG_WORKSPACE, then TF_WORKSPACE.")
	cmd.CommandLine.StringArrayVar(&vars, "var", nil, "Set an input variable, as NAME=VALUE. Can be used more than once.")
	cmd.CommandLine.StringArrayVar(&varFiles, "var-file", nil, "Load input variables from a file. Can be used more than once.")
	cmd.CommandLine.StringArrayVar(&args, "arg", nil, "Pass another option to the plan command, such as -arg=-parallelism=5. Can be used more than once.")
	cmd.CommandLine.BoolVar(&noComment, "no-comment", false, "Don't comment on the pull request.")
	cmd.CommandLine.BoolVar(&noOutput, "no-output", false, "Show only the list of changes in comments and summaries, not the plan's text. Use this if your pipeline's readers shouldn't see planned values.")
	if name == "apply" {
		cmd.CommandLine.BoolVar(&allowPR, "allow-pull-request-apply", false, "Allow applying from a pull request. Anyone who can open a pull request could then change your infrastructure.")
		cmd.CommandLine.BoolVar(&allowDestroy, "allow-destroy", false, "Apply even if the plan destroys or replaces resources.")
	}
	if name == "plan" || name == "apply" {
		cmd.CommandLine.BoolVar(&checks, "checks", true, "Run the built-in checks, such as for public storage. Use -checks=false to turn them off.")
		cmd.CommandLine.StringVar(&skipChecks, "skip-checks", "", "Turn off the listed checks, separated by commas.")
	}
	if name == "plan" {
		cmd.CommandLine.BoolVar(&cost, "cost", false, "Estimate the monthly cost of the plan, using the AWS and Azure price lists.")
	}

	cmd.Run = func(meta Meta) int {
		binary, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: couldn't find the bogform executable: %s\n", err)
			return ci.ExitFailed
		}
		var skip []string
		for _, id := range strings.Split(skipChecks, ",") {
			if id = strings.TrimSpace(id); id == "" {
				continue
			}
			if id != "all" && !slices.Contains(ci.CheckIDs, id) {
				fmt.Fprintf(os.Stderr, "Warning: there's no check named %q. The checks are: %s\n", id, strings.Join(ci.CheckIDs, ", "))
			}
			skip = append(skip, id)
		}
		runner := ci.NewRunner(ci.Options{
			Binary:                binary,
			Version:               version.String(),
			Directory:             ciDisplayDirectory(),
			Workspace:             workspace,
			Vars:                  vars,
			VarFiles:              varFiles,
			Args:                  args,
			AllowPullRequestApply: allowPR,
			NoComment:             noComment,
			NoOutput:              noOutput,
			AllowDestroy:          allowDestroy,
			NoChecks:              !checks || slices.Contains(skip, "all"),
			SkipChecks:            skip,
			Cost:                  cost,
			Env:                   os.Environ(),
			Stdout:                os.Stdout,
			Stderr:                os.Stderr,
		})
		return run(runner, meta.CommandContext())
	}
	return cmd
}

// ciDisplayDirectory names the configuration relative to the repository, so that the same
// directory has the same identity on every run regardless of where the pipeline checks it out.
func ciDisplayDirectory() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "."
	}
	relative, err := filepath.Rel(strings.TrimSpace(string(out)), dir)
	if err != nil {
		return "."
	}
	return filepath.ToSlash(relative)
}

func ciInitCommander() Command {
	cmd := Command{
		Name:  "init",
		Short: "Add a pipeline to this repository",
		Long:  "Writes a pipeline file for GitHub Actions (github), GitLab CI (gitlab), Azure Pipelines (azure), or Bitbucket Pipelines (bitbucket). If you don't name a platform, Bogform works it out from the \"origin\" Git remote.\n\nRun this from the root of your repository. The file is yours to edit and doesn't depend on any Bogform service.",

		DiagsWithNewline: true,
	}
	var platform, branch, directory, environment, applyOn string
	var force, gitState, cost bool
	cmd.CommandLine.PositionalArg(&platform, "platform", true)
	cmd.CommandLine.StringVar(&branch, "branch", "main", "The branch to apply from.")
	cmd.CommandLine.StringVar(&directory, "directory", ".", "The directory containing your configuration, relative to the repository root.")
	cmd.CommandLine.StringVar(&environment, "environment", "production", "The name of the environment that protects applies. Use -environment= to leave it out, for example on a private GitHub repository without a paid plan.")
	cmd.CommandLine.StringVar(&applyOn, "apply-on", ci.ApplyManual, "When to apply: \"manual\" to apply only when someone runs the pipeline, or \"merge\" to apply every change merged to the branch.")
	cmd.CommandLine.BoolVar(&gitState, "git-state", false, "Set up the pipeline for the git state backend: write access to the repository, and the state passphrase from a secret.")
	cmd.CommandLine.BoolVar(&cost, "cost", false, "Estimate the monthly cost of each plan. On AWS, the plan role needs pricing:GetProducts, which ReadOnlyAccess includes.")
	cmd.CommandLine.BoolVar(&force, "force", false, "Replace the file if it already exists.")

	cmd.Run = func(Meta) int {
		if platform == "" {
			out, err := exec.Command("git", "remote", "get-url", "origin").Output()
			if err == nil {
				platform = ci.DetectPlatformFromRemote(strings.TrimSpace(string(out)))
			}
			if platform == "" {
				fmt.Fprintln(os.Stderr, "Error: couldn't work out your platform from the \"origin\" remote. Name one: bogform ci init github, gitlab, azure, or bitbucket.")
				return ci.ExitFailed
			}
		}
		opts := ci.ScaffoldOptions{
			Platform:      platform,
			Version:       version.String(),
			DefaultBranch: branch,
			Directory:     directory,
			Environment:   environment,
			ApplyOn:       applyOn,
			GitState:      gitState,
			Cost:          cost,
			Force:         force,
			Root:          ".",
		}
		path, err := ci.Scaffold(opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return ci.ExitFailed
		}
		fmt.Printf("Created %s.\n%s", path, ci.NextSteps(platform, opts))
		return ci.ExitOK
	}
	return cmd
}

func ciContextCommander() Command {
	cmd := Command{
		Name:  "context",
		Short: "Show the detected pipeline details",
		Long:  "Prints what Bogform detected about the current pipeline as JSON. This is useful for troubleshooting.",
	}
	cmd.Run = func(Meta) int {
		raw, err := json.MarshalIndent(ci.Detect(os.Getenv), "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return ci.ExitFailed
		}
		fmt.Println(string(raw))
		return ci.ExitOK
	}
	return cmd
}
