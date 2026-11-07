// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// AzureQuery finds one price in the Azure Retail Prices API.
type AzureQuery struct {
	// Filter is the OData $filter expression.
	Filter string `json:"filter"`
	// ProductMatch lists text that the product name must contain, and ProductExcludes text it mustn't.
	ProductMatch    []string `json:"product_match,omitempty"`
	ProductExcludes []string `json:"product_excludes,omitempty"`
	// SkuExcludes lists text that the SKU name mustn't contain, such as "Spot".
	SkuExcludes []string `json:"sku_excludes,omitempty"`
	// MeterName, if set, must equal the meter name, and MeterSuffix must end it.
	MeterName   string `json:"meter_name,omitempty"`
	MeterSuffix string `json:"meter_suffix,omitempty"`
	// Unit, if set, must equal the unit of measure, such as "1 Hour".
	Unit string `json:"unit,omitempty"`
}

// AzureRetailPricesURL is the public Azure Retail Prices API, which needs no authentication.
const AzureRetailPricesURL = "https://prices.azure.com/api/retail/prices"

// AzureClient looks up prices with the Azure Retail Prices API.
type AzureClient struct {
	BaseURL string
	HTTP    *http.Client
	// RetryDelay is the base wait before retrying a rate-limited request.
	RetryDelay time.Duration
	cache      *Cache
}

// NewAzureClient returns a client for the public API.
func NewAzureClient(cache *Cache) *AzureClient {
	return &AzureClient{BaseURL: AzureRetailPricesURL, HTTP: &http.Client{Timeout: 30 * time.Second}, RetryDelay: 2 * time.Second, cache: cache}
}

type azureItem struct {
	RetailPrice   float64 `json:"retailPrice"`
	ProductName   string  `json:"productName"`
	SkuName       string  `json:"skuName"`
	MeterName     string  `json:"meterName"`
	UnitOfMeasure string  `json:"unitOfMeasure"`
	Type          string  `json:"type"`
}

type azurePage struct {
	Items        []azureItem `json:"Items"`
	NextPageLink string      `json:"NextPageLink"`
}

// Price returns the unit price for a query.
func (c *AzureClient) Price(ctx context.Context, query AzureQuery) (float64, error) {
	key, _ := json.Marshal(struct {
		Cloud string
		Query AzureQuery
	}{"azure", query})
	if price, ok := c.cache.Get(string(key)); ok {
		return price, nil
	}

	next := c.BaseURL + "?api-version=2023-01-01-preview&$filter=" + url.QueryEscape(query.Filter)
	var items []azureItem
	for page := 0; page < maxPricePages && next != ""; page++ {
		var result azurePage
		if err := c.get(ctx, next, &result); err != nil {
			return 0, err
		}
		items = append(items, result.Items...)
		// Only follow links to the same service.
		if result.NextPageLink != "" && !strings.HasPrefix(result.NextPageLink, c.BaseURL) {
			break
		}
		next = result.NextPageLink
	}
	price, err := selectAzurePrice(items, query)
	if err != nil {
		return 0, err
	}
	c.cache.Put(string(key), price)
	return price, nil
}

func (c *AzureClient) get(ctx context.Context, target string, out any) error {
	// The API limits how fast it can be called, so wait and retry when it says so.
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "bogform-ci")
		res, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("couldn't look up Azure prices: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if err != nil {
			return fmt.Errorf("couldn't look up Azure prices: %w", err)
		}
		if (res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500) && attempt < 3 {
			delay := c.RetryDelay * time.Duration(attempt+1)
			if seconds, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 30 {
				delay = time.Duration(seconds) * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("couldn't look up Azure prices: the API returned %d", res.StatusCode)
		}
		return json.Unmarshal(body, out)
	}
}

func selectAzurePrice(items []azureItem, query AzureQuery) (float64, error) {
	for _, item := range items {
		if item.Type != "" && item.Type != "Consumption" {
			continue
		}
		if !containsAll(item.ProductName, query.ProductMatch) || containsAny(item.ProductName, query.ProductExcludes) || containsAny(item.SkuName, query.SkuExcludes) {
			continue
		}
		if query.MeterName != "" && item.MeterName != query.MeterName {
			continue
		}
		if query.MeterSuffix != "" && !strings.HasSuffix(item.MeterName, query.MeterSuffix) {
			continue
		}
		if query.Unit != "" && item.UnitOfMeasure != query.Unit {
			continue
		}
		return item.RetailPrice, nil
	}
	return 0, fmt.Errorf("no Azure price was found for %s", query.Filter)
}

func containsAll(text string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}

func containsAny(text string, parts []string) bool {
	for _, part := range parts {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}
