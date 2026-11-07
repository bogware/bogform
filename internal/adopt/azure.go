// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package adopt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
)

// AzureResource is a resource as Azure lists it.
type AzureResource struct {
	ID, Type, Name, Kind string
}

// AzureLister lists a resource group and its resources.
type AzureLister interface {
	// ResourceGroup returns the group's name as Azure stores it.
	ResourceGroup(ctx context.Context, name string) (string, error)
	Resources(ctx context.Context, group string) ([]AzureResource, error)
}

type armLister struct {
	groups    *armresources.ResourceGroupsClient
	resources *armresources.Client
}

// NewAzureLister signs in with the default credential chain, which includes the Azure CLI's
// login, environment variables and managed identities.
func NewAzureLister(subscription string) (AzureLister, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("couldn't find Azure credentials. Run \"az login\": %w", err)
	}
	groups, err := armresources.NewResourceGroupsClient(subscription, cred, nil)
	if err != nil {
		return nil, err
	}
	resources, err := armresources.NewClient(subscription, cred, nil)
	if err != nil {
		return nil, err
	}
	return &armLister{groups: groups, resources: resources}, nil
}

func (l *armLister) ResourceGroup(ctx context.Context, name string) (string, error) {
	group, err := l.groups.Get(ctx, name, nil)
	if err != nil {
		return "", err
	}
	if group.Name == nil {
		return name, nil
	}
	return *group.Name, nil
}

func (l *armLister) Resources(ctx context.Context, group string) ([]AzureResource, error) {
	var out []AzureResource
	pager := l.resources.NewListByResourceGroupPager(group, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Value {
			item := AzureResource{}
			if r.ID != nil {
				item.ID = *r.ID
			}
			if r.Type != nil {
				item.Type = *r.Type
			}
			if r.Name != nil {
				item.Name = *r.Name
			}
			if r.Kind != nil {
				item.Kind = *r.Kind
			}
			out = append(out, item)
		}
	}
	return out, nil
}

// AzureSubscription finds the subscription to use: the given one, AZURE_SUBSCRIPTION_ID or
// ARM_SUBSCRIPTION_ID, or the Azure CLI's current subscription.
func AzureSubscription(ctx context.Context, given string) (string, error) {
	if given != "" {
		return given, nil
	}
	for _, name := range []string{"AZURE_SUBSCRIPTION_ID", "ARM_SUBSCRIPTION_ID"} {
		if v := os.Getenv(name); v != "" {
			return v, nil
		}
	}
	if _, err := exec.LookPath("az"); err == nil {
		out, err := exec.CommandContext(ctx, "az", "account", "show", "--query", "id", "--output", "tsv").Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", errors.New("no Azure subscription is set. Use -subscription, set AZURE_SUBSCRIPTION_ID, or run \"az account set\"")
}

// DiscoverAzure finds the resources in a resource group, and the group itself.
func DiscoverAzure(ctx context.Context, l AzureLister, subscription, group string) (*Result, error) {
	if group == "" {
		return nil, errors.New("use -resource-group to choose which resources to adopt")
	}
	name, err := l.ResourceGroup(ctx, group)
	if err != nil {
		return nil, fmt.Errorf("couldn't read resource group %q: %w", group, err)
	}
	resources, err := l.Resources(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("couldn't list the resources in %q: %w", name, err)
	}
	res := &Result{}
	res.add(ResourceGroup(subscription, name))
	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
	for _, item := range resources {
		r, ok, reason := MapAzure(item.ID, item.Type, item.Name, item.Kind)
		if !ok {
			res.skip(item.Type+" "+item.Name, reason)
			continue
		}
		res.add(r)
	}
	return res, nil
}
