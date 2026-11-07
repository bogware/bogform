// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package cost estimates the monthly cost of the resources in a plan, using the providers' public
// price lists. It covers resources with a fixed hourly or monthly price, and lists the rest as not
// priced, with the reason.
package cost

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// HoursPerMonth is the number of hours that cloud providers bill as a month.
const HoursPerMonth = 730

// Resource is a managed resource from a plan. Before and After are its attribute values, or nil if it
// doesn't exist before or after the change. Values that aren't known until apply are nil.
type Resource struct {
	Address       string
	Type          string
	ProviderName  string
	Actions       []string
	Before, After map[string]any
}

// Estimate is the result for a whole plan, in US dollars per month.
type Estimate struct {
	Currency  string      `json:"currency"`
	Before    float64     `json:"monthly_before"`
	After     float64     `json:"monthly_after"`
	Resources []Line      `json:"resources,omitempty"`
	NotPriced []NotPriced `json:"not_priced,omitempty"`
	Warnings  []string    `json:"warnings,omitempty"`
}

// Delta is the change in monthly cost.
func (e *Estimate) Delta() float64 { return e.After - e.Before }

// Line is a resource whose cost the plan changes.
type Line struct {
	Address     string  `json:"address"`
	Description string  `json:"description"`
	Before      float64 `json:"monthly_before"`
	After       float64 `json:"monthly_after"`
}

// NotPriced is a resource the plan creates or changes that isn't in the estimate.
type NotPriced struct {
	Address string `json:"address"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
}

// Pricer looks up a unit price in US dollars.
type Pricer[Q any] interface {
	Price(ctx context.Context, query Q) (float64, error)
}

// Estimator prices resources. A nil pricer means that provider's prices are unavailable, and its
// resources are listed as not priced.
type Estimator struct {
	AWS   Pricer[AWSQuery]
	Azure Pricer[AzureQuery]
	// AWSUnavailable and AzureUnavailable explain why a pricer is nil.
	AWSUnavailable, AzureUnavailable string
	// AWSRegion is the region for AWS resources whose values don't say. It comes from the provider
	// configuration or the environment.
	AWSRegion string
}

// Estimate prices every AWS and Azure resource in a plan, before and after the change.
func (e *Estimator) Estimate(ctx context.Context, resources []Resource) *Estimate {
	result := &Estimate{Currency: "USD"}
	warned := map[string]bool{}
	warn := func(message string) {
		if !warned[message] {
			warned[message] = true
			result.Warnings = append(result.Warnings, message)
		}
	}

	for _, resource := range resources {
		cloud := cloudOf(resource.ProviderName)
		if cloud == "" {
			continue
		}
		changed := slices.Contains(resource.Actions, "create") || slices.Contains(resource.Actions, "update")

		var description, reason string
		var costs [2]float64
		for side, values := range []map[string]any{resource.Before, resource.After} {
			if values == nil {
				continue
			}
			spec := describe(cloud, resource.Type, values, e.AWSRegion)
			if spec.kind != priceable {
				if side == 1 {
					reason = spec.reason
				}
				continue
			}
			total, err := e.price(ctx, cloud, spec.parts)
			if err != nil {
				if side == 1 {
					reason = err.Error()
				}
				warn(err.Error())
				continue
			}
			costs[side] = total
			if side == 1 || description == "" {
				description = spec.description
			}
		}

		if reason != "" && changed {
			result.NotPriced = append(result.NotPriced, NotPriced{Address: resource.Address, Type: resource.Type, Reason: reason})
		}
		result.Before += costs[0]
		result.After += costs[1]
		if round(costs[0]) != round(costs[1]) {
			result.Resources = append(result.Resources, Line{Address: resource.Address, Description: description, Before: costs[0], After: costs[1]})
		}
	}
	sort.SliceStable(result.Resources, func(i, j int) bool {
		return math.Abs(result.Resources[i].After-result.Resources[i].Before) > math.Abs(result.Resources[j].After-result.Resources[j].Before)
	})
	return result
}

func (e *Estimator) price(ctx context.Context, cloud string, parts []part) (float64, error) {
	total := 0.0
	for _, p := range parts {
		var unit float64
		var err error
		switch cloud {
		case cloudAWS:
			if e.AWS == nil {
				return 0, fmt.Errorf("AWS prices are unavailable: %s", cmpOr(e.AWSUnavailable, "no price list access"))
			}
			unit, err = e.AWS.Price(ctx, *p.aws)
		case cloudAzure:
			if e.Azure == nil {
				return 0, fmt.Errorf("Azure prices are unavailable: %s", cmpOr(e.AzureUnavailable, "no price list access"))
			}
			unit, err = e.Azure.Price(ctx, *p.azure)
		}
		if err != nil {
			return 0, err
		}
		total += unit * p.quantity
	}
	return total, nil
}

func round(value float64) float64 { return math.Round(value*100) / 100 }

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

const (
	cloudAWS   = "aws"
	cloudAzure = "azure"
)

func cloudOf(providerName string) string {
	switch {
	case providerName == "aws" || strings.HasSuffix(providerName, "/aws"):
		return cloudAWS
	case providerName == "azurerm" || strings.HasSuffix(providerName, "/azurerm"):
		return cloudAzure
	}
	return ""
}

// FormatMoney formats a monthly amount, such as "$1,234.56".
func FormatMoney(amount float64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	cents := int64(math.Round(amount * 100))
	whole := fmt.Sprintf("%d", cents/100)
	var grouped strings.Builder
	for i, digit := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return fmt.Sprintf("%s$%s.%02d", sign, grouped.String(), cents%100)
}

// FormatDelta formats a change, with a plus sign for an increase.
func FormatDelta(amount float64) string {
	if round(amount) > 0 {
		return "+" + FormatMoney(amount)
	}
	if round(amount) == 0 {
		return FormatMoney(0)
	}
	return FormatMoney(amount)
}
