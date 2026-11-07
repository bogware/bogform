// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bogware/bogform/internal/adopt"
)

const adoptLong = `Finds existing resources and writes an "import" block for each one, so that this configuration can take them over. Nothing in your cloud is changed: discovery only reads.

Next, run "bogform plan -generate-config-out=generated.tf" to write configuration for them, then review and tidy it, and plan again until the plan only imports. After the first apply, the import blocks have no effect, and you can remove them.

Import IDs appear in plan output. An ID that looks like an email address is left out and listed, so that it doesn't end up in a pull request comment.`

// AdoptCommander builds the "adopt" command.
func AdoptCommander() Command {
	return Command{
		Name:  "adopt",
		Short: "Write import blocks for existing resources",
		Long:  adoptLong,
		Commands: []Command{
			adoptAWSCommander(),
			adoptAzureCommander(),
		},
	}
}

type adoptOutput struct {
	out      string
	force    bool
	generate bool
}

func bindAdoptOutput(cmd *Command, o *adoptOutput) {
	cmd.CommandLine.StringVar(&o.out, "out", "imports.tf", "The file to write the import blocks to.").SetDisplay("=FILE")
	cmd.CommandLine.BoolVar(&o.force, "force", false, "Replace the file if it already exists.")
	cmd.CommandLine.BoolVar(&o.generate, "generate", false, `Afterwards, run "bogform plan -generate-config-out=generated.tf" in the file's directory to write configuration for the resources.`)
}

func adoptAWSCommander() Command {
	cmd := Command{
		Name:             "aws",
		Short:            "Find AWS resources by tag or name prefix",
		Long:             adoptLong + "\n\nWith -tag, resources are found with the Resource Groups Tagging API. With -prefix, supported resource types are listed with the Cloud Control API, and matched by name. With both, a resource must match both. Credentials and the region come from the usual AWS settings, such as AWS_PROFILE and AWS_REGION. The identity needs read-only access, such as the ReadOnlyAccess policy.",
		DiagsWithNewline: true,
	}
	var region, prefix string
	var tags []string
	var o adoptOutput
	cmd.CommandLine.StringVar(&region, "region", "", "The region to search. Global services, such as CloudFront and IAM, are always included.").SetDisplay("=REGION")
	cmd.CommandLine.StringArrayVar(&tags, "tag", nil, "Find resources with this tag, as KEY=VALUE, or KEY for any value. Can be used more than once: resources must have every tag.").SetDisplay("=KEY=VALUE")
	cmd.CommandLine.StringVar(&prefix, "prefix", "", "Find resources whose names start with this.").SetDisplay("=PREFIX")
	bindAdoptOutput(&cmd, &o)
	cmd.Run = func(meta Meta) int {
		ctx := meta.CommandContext()
		parsed := map[string]string{}
		for _, t := range tags {
			k, v, _ := strings.Cut(t, "=")
			if k == "" {
				fmt.Fprintf(os.Stderr, "Error: %q isn't a valid -tag. Use KEY=VALUE or KEY.\n", t)
				return 1
			}
			parsed[k] = v
		}
		if len(parsed) == 0 && prefix == "" {
			fmt.Fprintln(os.Stderr, "Error: use -tag, -prefix, or both, to choose which resources to adopt.")
			return 1
		}
		clients, err := adopt.NewAWSClients(ctx, region)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "Searching AWS account %s in %s...\n", clients.Account, clients.Region)
		res, err := adopt.DiscoverAWS(ctx, clients, adopt.AWSOptions{Region: clients.Region, Tags: parsed, Prefix: prefix})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		return finishAdopt(ctx, os.Stdout, os.Stderr, res, o, "bogform adopt aws "+describeAdoptArgs(region, tags, prefix))
	}
	return cmd
}

func describeAdoptArgs(region string, tags []string, prefix string) string {
	var parts []string
	if region != "" {
		parts = append(parts, "-region="+region)
	}
	for _, t := range tags {
		parts = append(parts, "-tag="+t)
	}
	if prefix != "" {
		parts = append(parts, "-prefix="+prefix)
	}
	return strings.Join(parts, " ")
}

func adoptAzureCommander() Command {
	cmd := Command{
		Name:             "azure",
		Short:            "Find the Azure resources in a resource group",
		Long:             adoptLong + "\n\nCredentials come from the Azure CLI's login, environment variables, or a managed identity. The identity needs the Reader role on the resource group.",
		DiagsWithNewline: true,
	}
	var subscription, group string
	var o adoptOutput
	cmd.CommandLine.StringVar(&subscription, "subscription", "", "The subscription ID. Defaults to AZURE_SUBSCRIPTION_ID, then the Azure CLI's current subscription.").SetDisplay("=ID")
	cmd.CommandLine.StringVar(&group, "resource-group", "", "The resource group to adopt, with everything in it. Required.").SetDisplay("=NAME")
	bindAdoptOutput(&cmd, &o)
	cmd.Run = func(meta Meta) int {
		ctx := meta.CommandContext()
		if group == "" {
			fmt.Fprintln(os.Stderr, "Error: use -resource-group to choose which resources to adopt.")
			return 1
		}
		sub, err := adopt.AzureSubscription(ctx, subscription)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		lister, err := adopt.NewAzureLister(sub)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		fmt.Fprintf(os.Stdout, "Searching resource group %q in subscription %s...\n", group, sub)
		res, err := adopt.DiscoverAzure(ctx, lister, sub, group)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			return 1
		}
		return finishAdopt(ctx, os.Stdout, os.Stderr, res, o, "bogform adopt azure -resource-group="+group)
	}
	return cmd
}

// finishAdopt writes the import blocks and reports what was skipped.
func finishAdopt(ctx context.Context, stdout, stderr io.Writer, res *adopt.Result, o adoptOutput, command string) int {
	for _, w := range res.Warnings {
		fmt.Fprintf(stderr, "Warning: %s\n", w)
	}
	if len(res.Resources) == 0 {
		fmt.Fprintln(stdout, "No supported resources were found, so no file was written.")
		writeSkipped(stdout, res.Skipped)
		return 1
	}

	if _, err := os.Stat(o.out); err == nil && !o.force {
		fmt.Fprintf(stderr, "Error: %s already exists. Use -out to write somewhere else, or -force to replace it.\n", o.out)
		return 1
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "Error: %s\n", err)
		return 1
	}

	header := "Written by \"" + command + "\".\n\n" +
		"Each block adopts an existing resource. Run \"bogform plan -generate-config-out=generated.tf\" to\n" +
		"write configuration for them. After the first apply, these blocks have no effect, and you can\n" +
		"remove them."
	var buf bytes.Buffer
	addrs, err := adopt.WriteImports(&buf, header, res.Resources)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %s\n", err)
		return 1
	}
	if err := os.WriteFile(o.out, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintf(stderr, "Error: couldn't write %s: %s\n", o.out, err)
		return 1
	}
	blocks := "import blocks"
	if len(addrs) == 1 {
		blocks = "import block"
	}
	fmt.Fprintf(stdout, "\nWrote %d %s to %s:\n", len(addrs), blocks, o.out)
	for _, a := range addrs {
		fmt.Fprintf(stdout, "  %s\n", a)
	}
	writeSkipped(stdout, res.Skipped)

	dir := filepath.Dir(o.out)
	if !o.generate {
		fmt.Fprintln(stdout, "\nNext, write configuration for them, then review it and plan until the plan only imports:")
		if dir != "." {
			fmt.Fprintf(stdout, "  cd %s\n", dir)
		}
		fmt.Fprintln(stdout, "  bogform init")
		fmt.Fprintln(stdout, "  bogform plan -generate-config-out=generated.tf")
		return 0
	}

	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "Error: couldn't find the bogform executable: %s\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "\nRunning bogform plan -generate-config-out=generated.tf...")
	plan := exec.CommandContext(ctx, binary, "plan", "-input=false", "-generate-config-out=generated.tf")
	plan.Dir = dir
	plan.Stdout, plan.Stderr = stdout, stderr
	if err := plan.Run(); err != nil {
		fmt.Fprintln(stderr, "\nThe plan didn't succeed. That's common for generated configuration: fix the errors it reports in generated.tf, then plan again.")
		return 1
	}
	return 0
}

func writeSkipped(w io.Writer, skipped []adopt.Skipped) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(w, "\nNot adopted (%d):\n", len(skipped))
	for _, s := range skipped {
		fmt.Fprintf(w, "  %s: %s\n", s.Source, s.Reason)
	}
}
