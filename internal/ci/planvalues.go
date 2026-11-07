// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// plannedResource is a managed resource from a plan, with its attribute values. Checks and cost
// estimates read the values in memory. They're never written to a report, because they can include
// sensitive data.
type plannedResource struct {
	Address      string
	Type         string
	ProviderName string
	Actions      []string
	// Before and After are nil when the resource doesn't exist before or after the change. Values that
	// aren't known until apply are nil too.
	Before, After map[string]any
}

// changed reports whether the plan creates or updates the resource.
func (r plannedResource) changed() bool {
	return slices.Contains(r.Actions, "create") || slices.Contains(r.Actions, "update")
}

// planValues is the part of the "show -json" format with attribute values.
type planValues struct {
	ResourceChanges []struct {
		Address      string `json:"address"`
		Mode         string `json:"mode"`
		Type         string `json:"type"`
		ProviderName string `json:"provider_name"`
		Change       struct {
			Actions []string       `json:"actions"`
			Before  map[string]any `json:"before"`
			After   map[string]any `json:"after"`
		} `json:"change"`
	} `json:"resource_changes"`
	Configuration struct {
		ProviderConfig map[string]struct {
			Name        string `json:"name"`
			Expressions map[string]struct {
				ConstantValue any `json:"constant_value"`
			} `json:"expressions"`
		} `json:"provider_config"`
	} `json:"configuration"`
}

// readPlanValues returns the managed resources in a plan, and the region set in the default
// configuration of the AWS provider, if it's a constant.
func readPlanValues(raw []byte) ([]plannedResource, string, error) {
	var plan planValues
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, "", fmt.Errorf("couldn't read the plan: %w", err)
	}
	var resources []plannedResource
	for _, rc := range plan.ResourceChanges {
		if rc.Mode != "managed" {
			continue
		}
		resources = append(resources, plannedResource{
			Address:      rc.Address,
			Type:         rc.Type,
			ProviderName: rc.ProviderName,
			Actions:      rc.Change.Actions,
			Before:       rc.Change.Before,
			After:        rc.Change.After,
		})
	}
	region := ""
	if provider, ok := plan.Configuration.ProviderConfig["aws"]; ok {
		if value, ok := provider.Expressions["region"].ConstantValue.(string); ok {
			region = value
		}
	}
	return resources, region, nil
}

// Helpers for reading attribute values. Each returns its zero value when the attribute is missing,
// null, unknown, or of another type, so checks never act on values they don't know.

func stringValue(values map[string]any, name string) string {
	s, _ := values[name].(string)
	return s
}

func numberValue(values map[string]any, name string) (float64, bool) {
	n, ok := values[name].(float64)
	return n, ok
}

// boolValue returns the value of a boolean attribute, and whether it's known.
func boolValue(values map[string]any, name string) (value, known bool) {
	value, known = values[name].(bool)
	return value, known
}

func listValue(values map[string]any, name string) []any {
	list, _ := values[name].([]any)
	return list
}

// blocks returns the nested blocks of a type, which the JSON format represents as a list of objects.
func blocks(values map[string]any, name string) []map[string]any {
	var result []map[string]any
	for _, item := range listValue(values, name) {
		if block, ok := item.(map[string]any); ok {
			result = append(result, block)
		}
	}
	return result
}

func stringList(values map[string]any, name string) []string {
	var result []string
	for _, item := range listValue(values, name) {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

// tagValue returns a tag from AWS tags or Azure tags.
func tagValue(values map[string]any, key string) string {
	for _, attribute := range []string{"tags", "tags_all"} {
		if tags, ok := values[attribute].(map[string]any); ok {
			if value, ok := tags[key].(string); ok {
				return value
			}
		}
	}
	return ""
}

// providerIs reports whether a provider address names the given provider type, such as "aws" for
// registry.opentofu.org/hashicorp/aws.
func providerIs(providerName, providerType string) bool {
	return providerName == providerType || strings.HasSuffix(providerName, "/"+providerType)
}
