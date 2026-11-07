// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package adopt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	taggingtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// AWSOptions selects which AWS resources to adopt.
type AWSOptions struct {
	// Region is the region to search. Global services are always searched in us-east-1.
	Region string
	// Tags select resources that have all of these tags. A "" value matches any value.
	Tags map[string]string
	// Prefix selects resources whose names start with it.
	Prefix string
}

// TaggingAPI is the part of the Resource Groups Tagging API that adopt uses.
type TaggingAPI interface {
	GetResources(context.Context, *resourcegroupstaggingapi.GetResourcesInput, ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

// CloudControlAPI is the part of the Cloud Control API that adopt uses.
type CloudControlAPI interface {
	ListResources(context.Context, *cloudcontrol.ListResourcesInput, ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error)
}

// AWSClients are the API clients for one region, and for us-east-1, where global services live.
type AWSClients struct {
	Region, Account     string
	Tagging, GlobalTags TaggingAPI
	Cloud, GlobalCloud  CloudControlAPI
}

// NewAWSClients creates clients from the default credential chain.
func NewAWSClients(ctx context.Context, region string) (*AWSClients, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("couldn't load AWS credentials: %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New("no AWS region is set. Use -region, or set AWS_REGION")
	}
	identity, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't sign in to AWS: %w", err)
	}
	global := cfg.Copy()
	global.Region = "us-east-1"
	return &AWSClients{
		Region:      cfg.Region,
		Account:     aws.ToString(identity.Account),
		Tagging:     resourcegroupstaggingapi.NewFromConfig(cfg),
		GlobalTags:  resourcegroupstaggingapi.NewFromConfig(global),
		Cloud:       cloudcontrol.NewFromConfig(cfg),
		GlobalCloud: cloudcontrol.NewFromConfig(global),
	}, nil
}

// DiscoverAWS finds resources by tag, by name prefix, or both. With both, a resource must
// match both.
func DiscoverAWS(ctx context.Context, c *AWSClients, opts AWSOptions) (*Result, error) {
	if len(opts.Tags) == 0 && opts.Prefix == "" {
		return nil, errors.New("use -tag, -prefix, or both, to choose which resources to adopt")
	}
	res := &Result{}

	lowerPrefix := strings.ToLower(opts.Prefix)
	if len(opts.Tags) > 0 {
		arns, err := taggedARNs(ctx, c, opts.Tags, res)
		if err != nil {
			return nil, err
		}
		for _, arn := range arns {
			r, ok, reason := MapARN(arn)
			switch {
			case !ok:
				res.skip(arn, reason)
			case matchesPrefix(r.Name, lowerPrefix):
				res.add(r)
			}
		}
	} else {
		for _, f := range listByPrefix(ctx, c, opts.Prefix, res) {
			res.add(f.resource)
		}
	}
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Source < res.Skipped[j].Source })
	return res, nil
}

func taggedARNs(ctx context.Context, c *AWSClients, tags map[string]string, res *Result) ([]string, error) {
	var filters []taggingtypes.TagFilter
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := taggingtypes.TagFilter{Key: aws.String(k)}
		if v := tags[k]; v != "" {
			f.Values = []string{v}
		}
		filters = append(filters, f)
	}

	seen := map[string]bool{}
	var arns []string
	clients := []TaggingAPI{c.Tagging}
	if c.Region != "us-east-1" && c.GlobalTags != nil {
		clients = append(clients, c.GlobalTags)
	}
	for i, client := range clients {
		p := resourcegroupstaggingapi.NewGetResourcesPaginator(client, &resourcegroupstaggingapi.GetResourcesInput{TagFilters: filters})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				if i == 0 {
					return nil, fmt.Errorf("couldn't search for tagged resources: %w", err)
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf("couldn't search us-east-1 for global resources: %s", err))
				break
			}
			for _, m := range page.ResourceTagMappingList {
				arn := aws.ToString(m.ResourceARN)
				if arn == "" || seen[arn] {
					continue
				}
				// Global services are found in us-east-1 only; skip that region's other resources.
				if i > 0 {
					if a, err := ParseARN(arn); err == nil && a.Region != "" {
						continue
					}
				}
				seen[arn] = true
				arns = append(arns, arn)
			}
		}
	}
	sort.Strings(arns)
	return arns, nil
}

type prefixMatch struct {
	arn      string
	resource Resource
}

func listByPrefix(ctx context.Context, c *AWSClients, prefix string, res *Result) []prefixMatch {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		matches []prefixMatch
		limit   = make(chan struct{}, 4)
	)
	lower := strings.ToLower(prefix)
	for _, t := range CloudControlTypes {
		client, region := c.Cloud, c.Region
		if t.Global {
			client, region = c.GlobalCloud, "us-east-1"
		}
		if client == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			var found []prefixMatch
			var warning string
			p := cloudcontrol.NewListResourcesPaginator(client, &cloudcontrol.ListResourcesInput{TypeName: aws.String(t.Name)})
			for p.HasMorePages() {
				page, err := p.NextPage(ctx)
				if err != nil {
					warning = fmt.Sprintf("couldn't list %s: %s", t.Name, shortAWSError(err))
					break
				}
				for _, d := range page.ResourceDescriptions {
					id := aws.ToString(d.Identifier)
					name := ""
					if t.nameProperty != "" {
						name = stringProperty(aws.ToString(d.Properties), t.nameProperty)
					}
					r, matchName, ok, _ := MapCloudControl(t, region, c.Account, id, name)
					if !ok || !matchesPrefix(matchName, lower) {
						continue
					}
					found = append(found, prefixMatch{arn: r.Source, resource: r})
				}
			}
			mu.Lock()
			defer mu.Unlock()
			matches = append(matches, found...)
			if warning != "" {
				res.Warnings = append(res.Warnings, warning)
			}
		}()
	}
	wg.Wait()
	sort.Strings(res.Warnings)
	sort.Slice(matches, func(i, j int) bool { return matches[i].arn < matches[j].arn })
	return matches
}

// matchesPrefix reports whether a name, or its last path segment, starts with prefix, so that a
// prefix like "app" also matches log groups like "/aws/lambda/app-forms".
func matchesPrefix(name, lowerPrefix string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, lowerPrefix) || strings.HasPrefix(lastSegment(name), lowerPrefix)
}

func stringProperty(properties, name string) string {
	var props map[string]any
	if json.Unmarshal([]byte(properties), &props) != nil {
		return ""
	}
	s, _ := props[name].(string)
	return s
}

// shortAWSError keeps an AWS error to its code and message, without request IDs.
func shortAWSError(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() + ": " + apiErr.ErrorMessage()
	}
	return err.Error()
}
