// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package cost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// AWSFilter narrows a Price List query. Contains matches part of the value instead of all of it.
type AWSFilter struct {
	Field    string `json:"field"`
	Value    string `json:"value"`
	Contains bool   `json:"contains,omitempty"`
}

// AWSQuery finds one on-demand price in the AWS Price List.
type AWSQuery struct {
	Service string      `json:"service"`
	Filters []AWSFilter `json:"filters"`
	// UsageType is a regular expression that the product's usage type must match.
	UsageType string `json:"usage_type,omitempty"`
	// UsageExclude is a regular expression that the usage type mustn't match.
	UsageExclude string `json:"usage_exclude,omitempty"`
	// Unit is the price dimension's unit, such as "Hrs". Empty matches any unit.
	Unit string `json:"unit,omitempty"`
	// Tiered picks the highest tier, which skips free tier allowances.
	Tiered bool `json:"tiered,omitempty"`
}

// pricingAPI is the part of the Price List Query API this package uses.
type pricingAPI interface {
	GetProducts(ctx context.Context, params *pricing.GetProductsInput, optFns ...func(*pricing.Options)) (*pricing.GetProductsOutput, error)
}

// AWSClient looks up prices with the Price List Query API, which needs pricing:GetProducts.
type AWSClient struct {
	api   pricingAPI
	cache *Cache
}

// NewAWSClient uses the default AWS credential chain. The Price List Query API is in us-east-1.
func NewAWSClient(ctx context.Context, cache *Cache, optFns ...func(*pricing.Options)) (*AWSClient, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"))
	if err != nil {
		return nil, err
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		return nil, errors.New("no AWS credentials were found")
	}
	return &AWSClient{api: pricing.NewFromConfig(cfg, optFns...), cache: cache}, nil
}

// maxPricePages limits how many pages of products a query reads. Queries are narrow, so a query that
// needs more is too broad to trust.
const maxPricePages = 10

// Price returns the unit price for a query.
func (c *AWSClient) Price(ctx context.Context, query AWSQuery) (float64, error) {
	key, _ := json.Marshal(struct {
		Cloud string
		Query AWSQuery
	}{"aws", query})
	if price, ok := c.cache.Get(string(key)); ok {
		return price, nil
	}

	input := &pricing.GetProductsInput{ServiceCode: aws.String(query.Service), MaxResults: aws.Int32(100)}
	for _, f := range query.Filters {
		filterType := types.FilterTypeTermMatch
		if f.Contains {
			filterType = types.FilterTypeContains
		}
		input.Filters = append(input.Filters, types.Filter{Field: aws.String(f.Field), Value: aws.String(f.Value), Type: filterType})
	}
	var products []string
	for page := 0; page < maxPricePages; page++ {
		out, err := c.api.GetProducts(ctx, input)
		if err != nil {
			return 0, fmt.Errorf("couldn't look up AWS prices: %w", err)
		}
		products = append(products, out.PriceList...)
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		input.NextToken = out.NextToken
	}
	price, err := selectAWSPrice(products, query)
	if err != nil {
		return 0, err
	}
	c.cache.Put(string(key), price)
	return price, nil
}

// awsProduct is the part of a Price List product that matters here.
type awsProduct struct {
	Product struct {
		Attributes map[string]string `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]struct {
				Unit         string            `json:"unit"`
				BeginRange   string            `json:"beginRange"`
				PricePerUnit map[string]string `json:"pricePerUnit"`
			} `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

// selectAWSPrice picks the query's price from the products that the API returned.
func selectAWSPrice(products []string, query AWSQuery) (float64, error) {
	var usage *regexp.Regexp
	if query.UsageType != "" {
		var err error
		if usage, err = regexp.Compile(query.UsageType); err != nil {
			return 0, err
		}
	}
	var exclude *regexp.Regexp
	if query.UsageExclude != "" {
		var err error
		if exclude, err = regexp.Compile(query.UsageExclude); err != nil {
			return 0, err
		}
	}
	type candidate struct {
		usageType string
		begin     float64
		price     float64
	}
	var candidates []candidate
	for _, raw := range products {
		var product awsProduct
		if err := json.Unmarshal([]byte(raw), &product); err != nil {
			return 0, fmt.Errorf("couldn't read an AWS price: %w", err)
		}
		usageType := product.Product.Attributes["usagetype"]
		if (usage != nil && !usage.MatchString(usageType)) || (exclude != nil && exclude.MatchString(usageType)) {
			continue
		}
		for _, term := range product.Terms.OnDemand {
			for _, dimension := range term.PriceDimensions {
				if query.Unit != "" && !strings.EqualFold(dimension.Unit, query.Unit) {
					continue
				}
				price, err := strconv.ParseFloat(dimension.PricePerUnit["USD"], 64)
				if err != nil {
					continue
				}
				begin, _ := strconv.ParseFloat(dimension.BeginRange, 64)
				candidates = append(candidates, candidate{usageType: usageType, begin: begin, price: price})
			}
		}
	}
	if len(candidates) == 0 {
		return 0, fmt.Errorf("no AWS price was found for %s", describeAWSQuery(query))
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].usageType != candidates[j].usageType {
			return candidates[i].usageType < candidates[j].usageType
		}
		if query.Tiered {
			return candidates[i].begin > candidates[j].begin
		}
		return candidates[i].begin < candidates[j].begin
	})
	return candidates[0].price, nil
}

func describeAWSQuery(query AWSQuery) string {
	var parts []string
	for _, f := range query.Filters {
		if f.Field == "instanceType" || f.Field == "regionCode" || f.Field == "volumeApiName" || f.Field == "productFamily" {
			parts = append(parts, f.Value)
		}
	}
	return query.Service + " (" + strings.Join(parts, ", ") + ")"
}
