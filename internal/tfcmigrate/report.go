// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package tfcmigrate

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// CIInitFlags suggests "bogform ci init" flags that keep the workspace's behavior.
func CIInitFlags(ws *Workspace) []string {
	var flags []string
	if dir := strings.Trim(ws.WorkingDirectory, "/"); dir != "" {
		flags = append(flags, "-directory="+dir)
	}
	if ws.AutoApply {
		flags = append(flags, "-apply-on=merge")
	} else {
		flags = append(flags, "-apply-on=manual")
	}
	if ws.VCSBranch != "" {
		flags = append(flags, "-branch="+ws.VCSBranch)
	}
	return flags
}

// envNameRE matches characters that can't appear in a pipeline secret's name.
var envNameRE = regexp.MustCompile(`[^A-Za-z0-9_]`)

// SecretName suggests the pipeline secret that should hold a variable.
func SecretName(v Variable) string {
	return strings.ToUpper(envNameRE.ReplaceAllString(v.Key, "_"))
}

// PipelineEnv says how a pipeline passes a variable to Bogform: Terraform variables through
// TF_VAR_<name>, and environment variables under their own name.
func PipelineEnv(v Variable) string {
	if v.Category == "terraform" {
		return "TF_VAR_" + v.Key
	}
	return v.Key
}

// cloudCredentialEnv lists environment variables that held static cloud credentials. In a
// pipeline, OIDC replaces them, so they shouldn't become secrets.
var cloudCredentialEnv = map[string]string{
	"AWS_ACCESS_KEY_ID":                  "aws",
	"AWS_SECRET_ACCESS_KEY":              "aws",
	"AWS_SESSION_TOKEN":                  "aws",
	"ARM_CLIENT_SECRET":                  "azure",
	"ARM_CLIENT_ID":                      "azure",
	"ARM_CLIENT_CERTIFICATE_PATH":        "azure",
	"GOOGLE_CREDENTIALS":                 "gcp",
	"GOOGLE_APPLICATION_CREDENTIALS":     "gcp",
	"TFC_AWS_PROVIDER_AUTH":              "hcp-oidc",
	"TFC_AWS_RUN_ROLE_ARN":               "hcp-oidc",
	"TFC_AZURE_PROVIDER_AUTH":            "hcp-oidc",
	"TFC_AZURE_RUN_CLIENT_ID":            "hcp-oidc",
	"TFC_GCP_PROVIDER_AUTH":              "hcp-oidc",
	"TFC_GCP_RUN_SERVICE_ACCOUNT_EMAIL":  "hcp-oidc",
	"TFC_GCP_WORKLOAD_PROVIDER_NAME":     "hcp-oidc",
	"TFC_VAULT_PROVIDER_AUTH":            "hcp-oidc",
	"TFC_WORKLOAD_IDENTITY_AUDIENCE":     "hcp-oidc",
	"TFC_DEFAULT_WORKLOAD_IDENTITY_TYPE": "hcp-oidc",
}

// WriteReport prints what a migration needs besides the state: settings, variables, and the
// next steps. Variable values are shown only when showValues is set, and never for sensitive
// variables.
func WriteReport(w io.Writer, ws *Workspace, vars []Variable, showValues bool) {
	fmt.Fprintf(w, "Workspace %q\n", ws.Name)
	row := func(label, value string) {
		if value == "" {
			value = "-"
		}
		fmt.Fprintf(w, "  %-20s %s\n", label, value)
	}
	row("Terraform version", ws.TerraformVersion)
	row("Execution mode", ws.ExecutionMode)
	row("Working directory", ws.WorkingDirectory)
	if ws.VCSRepository != "" {
		branch := ws.VCSBranch
		if branch == "" {
			branch = "default branch"
		}
		row("VCS repository", ws.VCSRepository+" ("+branch+")")
	} else {
		row("VCS repository", "")
	}
	row("Auto-apply", fmt.Sprintf("%t", ws.AutoApply))

	sorted := append([]Variable(nil), vars...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Category != sorted[j].Category {
			return sorted[i].Category > sorted[j].Category // terraform before env
		}
		return sorted[i].Key < sorted[j].Key
	})

	fmt.Fprintln(w)
	if len(sorted) == 0 {
		fmt.Fprintln(w, "Variables: none on the workspace. Check any variable sets it uses, which aren't listed here.")
	} else {
		fmt.Fprintln(w, "Variables (variable sets aren't listed):")
		for _, v := range sorted {
			kind := v.Category
			if v.HCL {
				kind += ", HCL"
			}
			if v.Sensitive {
				kind += ", sensitive"
			}
			line := fmt.Sprintf("  %-32s %s", v.Key, kind)
			if showValues && !v.Sensitive {
				line += " = " + v.Value
			}
			fmt.Fprintln(w, line)
		}
	}

	var secrets, plain, credentials []Variable
	for _, v := range sorted {
		switch {
		case v.Category == "env" && cloudCredentialEnv[strings.ToUpper(v.Key)] != "":
			credentials = append(credentials, v)
		case v.Sensitive:
			secrets = append(secrets, v)
		default:
			plain = append(plain, v)
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintf(w, "  1. Add a pipeline: bogform ci init %s\n", strings.Join(CIInitFlags(ws), " "))
	step := 2
	if len(secrets) > 0 {
		fmt.Fprintf(w, "  %d. Store these as pipeline secrets, and pass each one to Bogform as shown:\n", step)
		for _, v := range secrets {
			fmt.Fprintf(w, "       %-32s secret %s -> %s\n", v.Key, SecretName(v), PipelineEnv(v))
		}
		step++
	}
	if len(plain) > 0 {
		fmt.Fprintf(w, "  %d. Move these into the configuration (variable defaults or a .tfvars file), or into the pipeline's environment. Treat any that are personal, like email addresses, as secrets:\n", step)
		for _, v := range plain {
			fmt.Fprintf(w, "       %s\n", v.Key)
		}
		step++
	}
	if len(credentials) > 0 {
		fmt.Fprintf(w, "  %d. Don't copy these. They gave HCP Terraform access to your cloud. The pipeline signs in with OIDC instead, as its own identity, so any client ID it needs is the new identity's:\n", step)
		for _, v := range credentials {
			fmt.Fprintf(w, "       %s\n", v.Key)
		}
		step++
	}
	if ws.VCSRepository != "" {
		fmt.Fprintf(w, "  %d. Once the pipeline works, disconnect %s from the workspace, or delete the workspace, so that HCP Terraform stops running on new commits.\n", step, ws.VCSRepository)
	}
}
