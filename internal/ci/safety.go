// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bogware/bogform/internal/ci/cost"
)

// AllowDestroyLabel is the pull request label that lets an apply destroy or replace resources.
const AllowDestroyLabel = "allow-destroy"

// AllowDestroyEnvName is the variable that lets an apply destroy or replace resources.
const AllowDestroyEnvName = "BOG_CI_ALLOW_DESTROY"

// costTimeout limits how long price lookups can delay a plan.
const costTimeout = 90 * time.Second

// analyze runs the checks and the cost estimate. Neither can fail the run: problems are warnings.
func (r *Runner) analyze(ctx context.Context, report *Report, raw []byte) {
	if r.NoChecks && (!r.Cost || report.Kind != KindPlan) {
		return
	}
	resources, region, err := readPlanValues(raw)
	if err != nil {
		r.warn("couldn't check the plan: %s", err)
		return
	}
	if !r.NoChecks {
		report.Checks = runChecks(resources, r.SkipChecks)
	}
	if r.Cost && report.Kind == KindPlan {
		report.Cost = r.estimateCost(ctx, resources, region)
	}
}

func (r *Runner) estimateCost(ctx context.Context, resources []plannedResource, providerRegion string) *cost.Estimate {
	ctx, cancel := context.WithTimeout(ctx, costTimeout)
	defer cancel()

	var input []cost.Resource
	hasAWS, hasAzure := false, false
	for _, resource := range resources {
		hasAWS = hasAWS || providerIs(resource.ProviderName, "aws")
		hasAzure = hasAzure || providerIs(resource.ProviderName, "azurerm")
		input = append(input, cost.Resource{
			Address: resource.Address, Type: resource.Type, ProviderName: resource.ProviderName,
			Actions: resource.Actions, Before: resource.Before, After: resource.After,
		})
	}

	cache := cost.OpenCache(cost.DefaultCachePath())
	defer cache.Save()
	estimator := &cost.Estimator{AWSRegion: providerRegion}
	var warnings []string
	if hasAWS {
		if client, err := cost.NewAWSClient(ctx, cache); err != nil {
			estimator.AWSUnavailable = err.Error()
		} else {
			estimator.AWS = client
		}
		if estimator.AWSRegion == "" {
			estimator.AWSRegion = cmpOr(r.lookup("AWS_REGION"), r.lookup("AWS_DEFAULT_REGION"))
		}
		if estimator.AWSRegion == "" {
			estimator.AWSRegion = "us-east-1"
			warnings = append(warnings, "AWS resources without a region of their own are priced in us-east-1, because the AWS provider's region isn't a constant and AWS_REGION isn't set.")
		}
	}
	if hasAzure {
		estimator.Azure = cost.NewAzureClient(cache)
	}
	estimate := estimator.Estimate(ctx, input)
	estimate.Warnings = append(warnings, estimate.Warnings...)
	return estimate
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// destroyAllowed decides whether an apply may destroy or replace resources. It returns what allowed
// it, or an empty string and, if a check couldn't be made, why.
func (r *Runner) destroyAllowed(ctx context.Context) (allowedBy, why string) {
	if r.AllowDestroy {
		return "the -allow-destroy option", ""
	}
	for _, name := range []string{AllowDestroyEnvName, "TF_CI_ALLOW_DESTROY"} {
		if truthy(r.lookup(name)) {
			return "the " + AllowDestroyEnvName + " variable", ""
		}
	}
	if r.Context.Platform != PlatformGitHub {
		return "", ""
	}
	number, err := r.githubAllowDestroyLabel(ctx)
	if err != nil {
		return "", fmt.Sprintf("Couldn't check pull request labels: %s.", err)
	}
	if number != 0 {
		return fmt.Sprintf("the %s label on pull request #%d", inlineCode(AllowDestroyLabel), number), ""
	}
	return "", ""
}

// githubAllowDestroyLabel returns the number of a pull request for the current commit that has the
// allow-destroy label: one that was merged, or the pull request this run is for. It returns zero if
// there isn't one.
func (r *Runner) githubAllowDestroyLabel(ctx context.Context) (int, error) {
	if r.token == "" {
		return 0, fmt.Errorf("%s isn't set", TokenEnvName)
	}
	if r.Context.Commit == "" || r.Context.Repository == "" || r.Context.apiURL == "" {
		return 0, fmt.Errorf("the commit or repository isn't known")
	}
	client := r.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	api := &apiClient{client: client, setHeaders: func(h http.Header) {
		h.Set("Authorization", "Bearer "+r.token)
		h.Set("Accept", "application/vnd.github+json")
		h.Set("X-GitHub-Api-Version", "2022-11-28")
	}}
	type label struct {
		Name string `json:"name"`
	}
	var pulls []struct {
		Number   int     `json:"number"`
		MergedAt *string `json:"merged_at"`
		Labels   []label `json:"labels"`
	}
	target := fmt.Sprintf("%s/repos/%s/commits/%s/pulls", r.Context.apiURL, r.Context.Repository, r.Context.Commit)
	if err := api.do(ctx, http.MethodGet, target, nil, &pulls); err != nil {
		return 0, err
	}
	for _, pull := range pulls {
		current := r.Context.IsPullRequest && fmt.Sprint(pull.Number) == r.Context.PullRequest
		if pull.MergedAt == nil && !current {
			continue
		}
		if slices.Contains(pull.Labels, label{Name: AllowDestroyLabel}) {
			return pull.Number, nil
		}
	}
	return 0, nil
}

// refuseDestroyMessage explains why an apply was refused, and how to allow it.
func refuseDestroyMessage(report *Report, why string) string {
	destroyed := report.destroyedAddresses()
	var addresses []string
	for i, change := range destroyed {
		if i == 10 {
			addresses = append(addresses, fmt.Sprintf("and %d more", len(destroyed)-10))
			break
		}
		addresses = append(addresses, change.Address)
	}
	hint := allowDestroyHint(report.Run.Platform)
	if report.Run.Platform == PlatformGitHub {
		hint = "add the `allow-destroy` label to the merged pull request and run the workflow again, or run it with **allow destroy** ticked"
	}
	noun := "resources"
	if len(destroyed) == 1 {
		noun = "resource"
	}
	message := fmt.Sprintf("Refusing to apply, because the plan destroys or replaces %d %s: %s. To allow it, %s.",
		len(destroyed), noun, strings.Join(addresses, ", "), hint)
	if why != "" {
		message += " " + why
	}
	return message
}
