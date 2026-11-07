// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bogware/bogform/templates"
)

// BogformMDFile is where "ci init" writes the guide, relative to the repository root.
const BogformMDFile = "BOGFORM.md"

// repositorySectionHeading starts the part of BOGFORM.md that describes one repository.
const repositorySectionHeading = "## This repository"

// RenderBogformMD returns BOGFORM.md with its last section filled in from the pipeline's options.
// Details that the options don't include are left blank for the user.
func RenderBogformMD(opts ScaffoldOptions) (string, error) {
	opts, err := normalizeScaffoldOptions(opts)
	if err != nil {
		return "", err
	}
	guide := templates.BogformMD
	i := strings.Index(guide, repositorySectionHeading)
	if i < 0 {
		return "", errors.New("BOGFORM.md is missing its \"This repository\" section")
	}
	return guide[:i] + repositorySection(opts), nil
}

// WriteBogformMD writes BOGFORM.md at the repository root. It returns false, without an error, if the
// file exists and opts.Force isn't set: the file is usually edited after it's written.
func WriteBogformMD(opts ScaffoldOptions) (bool, error) {
	content, err := RenderBogformMD(opts)
	if err != nil {
		return false, err
	}
	path := filepath.Join(opts.Root, BogformMDFile)
	if !opts.Force {
		if _, err := os.Stat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

func repositorySection(opts ScaffoldOptions) string {
	directory := "The repository root"
	if opts.Directory != "." {
		directory = "`" + strings.TrimSuffix(opts.Directory, "/") + "/`"
	}

	rows := [][2]string{
		{"Infrastructure directory", directory},
		{"Pipeline", "`" + pipelineFiles[opts.Platform] + "`"},
		{"Applies", appliesDescription(opts)},
		{"Plan role", identityDescription(opts.signin("plan"))},
		{"Apply role", identityDescription(opts.signin("apply"))},
		{"Secrets", secretsDescription(opts)},
		{"Passphrase backup", ""},
		{"Keychain entry", ""},
		{"Access defined in", ""},
	}

	var out strings.Builder
	out.WriteString(repositorySectionHeading + "\n\n")
	out.WriteString("<!-- Written by \"bogform ci init\". Fill in anything left blank. -->\n\n")
	out.WriteString("| | |\n|---|---|\n")
	for _, row := range rows {
		fmt.Fprintf(&out, "| %s | %s |\n", row[0], row[1])
	}
	out.WriteString("\nAnything else specific to this repository:\n\n-\n")
	return out.String()
}

func appliesDescription(opts ScaffoldOptions) string {
	branch := "`" + opts.DefaultBranch + "`"
	var text string
	if opts.ApplyOn == ApplyMerge {
		text = "Automatically, when a pull request merges to " + branch + "."
	} else {
		switch opts.Platform {
		case PlatformGitHub:
			text = "Manually: run the **Bogform** workflow on " + branch + " from the Actions tab after merging."
		case PlatformGitLab:
			text = "Manually: start the `bogform-apply` job on " + branch + " from the pipeline page after merging."
		case PlatformBitbucket:
			text = "Manually: run the `apply` custom pipeline on " + branch + " after merging."
		default:
			text = "Manually: run the pipeline on " + branch + " after merging."
		}
	}
	if opts.Environment != "" {
		text += " Applies use the `" + opts.Environment + "` environment, which can require approval."
	}
	return text
}

func identityDescription(s signinOptions) string {
	var parts []string
	if s.AWSRole != "" {
		parts = append(parts, "`"+s.AWSRole+"`")
	}
	if s.AzureClientID != "" {
		parts = append(parts, "Azure client ID `"+s.AzureClientID+"`")
	}
	if s.AzureServiceConnection != "" {
		parts = append(parts, "Azure service connection `"+s.AzureServiceConnection+"`")
	}
	return strings.Join(parts, ", ")
}

func secretsDescription(opts ScaffoldOptions) string {
	var secrets []string
	if opts.GitState {
		switch opts.Platform {
		case PlatformGitHub:
			secrets = append(secrets, "`BOGFORM_STATE_PASSPHRASE` (`TF_VAR_state_passphrase`)")
		case PlatformAzure:
			secrets = append(secrets, "`STATE_PASSPHRASE` (`TF_VAR_state_passphrase`)")
		default:
			secrets = append(secrets, "`TF_VAR_state_passphrase`")
		}
	}
	if opts.Platform == PlatformGitLab || opts.Platform == PlatformBitbucket {
		secrets = append(secrets, "`BOG_CI_TOKEN`, to comment on pull requests")
	}
	return strings.Join(secrets, ", ")
}
