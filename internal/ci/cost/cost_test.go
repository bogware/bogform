// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package cost

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
)

const (
	awsProvider   = "registry.opentofu.org/hashicorp/aws"
	azureProvider = "registry.opentofu.org/hashicorp/azurerm"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// fixtureServer serves a recorded API response.
func fixtureServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func testAWSClient(t *testing.T, fixture string) *AWSClient {
	server := fixtureServer(t, fixture)
	api := pricing.New(pricing.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(server.URL),
		Credentials:  aws.AnonymousCredentials{},
	})
	return &AWSClient{api: api, cache: OpenCache("")}
}

func awsQuery(t *testing.T, resourceType string, values map[string]any, index int) AWSQuery {
	t.Helper()
	s := describeAWS(resourceType, values, "us-east-1")
	if s.kind != priceable || len(s.parts) <= index {
		t.Fatalf("%s isn't priceable: %+v", resourceType, s)
	}
	return *s.parts[index].aws
}

func TestAWSClient_recordedPrices(t *testing.T) {
	provisioned := map[string]any{"billing_mode": "PROVISIONED", "read_capacity": 5.0, "write_capacity": 5.0}
	infrequent := map[string]any{"billing_mode": "PROVISIONED", "read_capacity": 5.0, "write_capacity": 5.0, "table_class": "STANDARD_INFREQUENT_ACCESS"}
	tests := map[string]struct {
		fixture string
		query   AWSQuery
		want    float64
	}{
		"instance":          {"aws_ec2_t3_micro.json", awsQuery(t, "aws_instance", map[string]any{"instance_type": "t3.micro"}, 0), 0.0104},
		"NAT gateway hours": {"aws_nat_gateway.json", awsQuery(t, "aws_nat_gateway", map[string]any{}, 0), 0.045},
		"load balancer":     {"aws_alb.json", awsQuery(t, "aws_lb", map[string]any{"load_balancer_type": "application"}, 0), 0.0225},
		// The standard price beyond the free tier, not the free tier or infrequent access price.
		"read capacity":     {"aws_dynamodb_capacity.json", awsQuery(t, "aws_dynamodb_table", provisioned, 0), 0.00013},
		"write capacity":    {"aws_dynamodb_capacity.json", awsQuery(t, "aws_dynamodb_table", provisioned, 1), 0.00065},
		"infrequent access": {"aws_dynamodb_capacity.json", awsQuery(t, "aws_dynamodb_table", infrequent, 0), 0.00016},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := testAWSClient(t, test.fixture).Price(context.Background(), test.query)
			if err != nil {
				t.Fatal(err)
			}
			if !near(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestAWSClient_noMatch(t *testing.T) {
	query := awsQuery(t, "aws_instance", map[string]any{"instance_type": "t3.micro"}, 0)
	query.Unit = "GB-Mo"
	if _, err := testAWSClient(t, "aws_ec2_t3_micro.json").Price(context.Background(), query); err == nil || !strings.Contains(err.Error(), "no AWS price") {
		t.Fatalf("expected no price, got %v", err)
	}
}

func testAzureClient(t *testing.T, server *httptest.Server) *AzureClient {
	client := NewAzureClient(OpenCache(""))
	client.BaseURL = server.URL
	client.RetryDelay = time.Millisecond
	return client
}

func azureQuery(t *testing.T, resourceType string, values map[string]any, index int) AzureQuery {
	t.Helper()
	s := describeAzure(resourceType, values)
	if s.kind != priceable || len(s.parts) <= index {
		t.Fatalf("%s isn't priceable: %+v", resourceType, s)
	}
	return *s.parts[index].azure
}

func TestAzureClient_recordedPrices(t *testing.T) {
	tests := map[string]struct {
		fixture string
		query   AzureQuery
		want    float64
	}{
		"Linux VM":   {"azure_vm_b2s.json", azureQuery(t, "azurerm_linux_virtual_machine", map[string]any{"size": "Standard_B2s", "location": "East US"}, 0), 0.0416},
		"Windows VM": {"azure_vm_b2s.json", azureQuery(t, "azurerm_windows_virtual_machine", map[string]any{"size": "Standard_B2s", "location": "eastus"}, 0), 0.0496},
		// Of the managed disk, mount, and page blob prices, only the managed disk's.
		"disk":       {"azure_disk_p10.json", azureQuery(t, "azurerm_managed_disk", map[string]any{"storage_account_type": "Premium_LRS", "disk_size_gb": 100.0, "location": "eastus"}, 0), 19.71},
		"PostgreSQL": {"azure_postgres_ddsv5.json", azureQuery(t, "azurerm_postgresql_flexible_server", map[string]any{"sku_name": "GP_Standard_D2ds_v5", "location": "eastus"}, 0), 0.089},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := testAzureClient(t, fixtureServer(t, test.fixture)).Price(context.Background(), test.query)
			if err != nil {
				t.Fatal(err)
			}
			if !near(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestAzureClient_retriesRateLimits(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "azure_vm_b2s.json"))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client := testAzureClient(t, server)
	query := azureQuery(t, "azurerm_linux_virtual_machine", map[string]any{"size": "Standard_B2s", "location": "eastus"}, 0)
	if _, err := client.Price(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	// A second lookup comes from the cache.
	if _, err := client.Price(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one retry and then a cached price, got %d calls", calls.Load())
	}
}

// fakePricer prices queries by their key, a word from the query.
type fakePricer[Q any] struct {
	prices map[string]float64
	key    func(Q) string
}

func (f fakePricer[Q]) Price(_ context.Context, query Q) (float64, error) {
	if price, ok := f.prices[f.key(query)]; ok {
		return price, nil
	}
	return 0, errors.New("no price for " + f.key(query))
}

func awsKey(q AWSQuery) string {
	for _, f := range q.Filters {
		if f.Field == "instanceType" || f.Field == "volumeApiName" {
			return f.Value
		}
	}
	return q.Service
}

func TestEstimate(t *testing.T) {
	estimator := &Estimator{
		AWS:              fakePricer[AWSQuery]{prices: map[string]float64{"t3.micro": 0.01, "t3.large": 0.08, "gp3": 0.08, "AmazonEC2": 0.045}, key: awsKey},
		AzureUnavailable: "testing",
		AWSRegion:        "us-east-1",
	}
	resources := []Resource{
		{Address: "aws_instance.new", Type: "aws_instance", ProviderName: awsProvider, Actions: []string{"create"},
			After: map[string]any{"instance_type": "t3.micro"}},
		{Address: "aws_instance.resized", Type: "aws_instance", ProviderName: awsProvider, Actions: []string{"update"},
			Before: map[string]any{"instance_type": "t3.micro"}, After: map[string]any{"instance_type": "t3.large"}},
		{Address: "aws_ebs_volume.same", Type: "aws_ebs_volume", ProviderName: awsProvider, Actions: []string{"no-op"},
			Before: map[string]any{"type": "gp3", "size": 10.0}, After: map[string]any{"type": "gp3", "size": 10.0}},
		{Address: "aws_nat_gateway.gone", Type: "aws_nat_gateway", ProviderName: awsProvider, Actions: []string{"delete"},
			Before: map[string]any{}},
		{Address: "aws_instance.unknown", Type: "aws_instance", ProviderName: awsProvider, Actions: []string{"create"},
			After: map[string]any{}},
		{Address: "aws_lambda_function.usage", Type: "aws_lambda_function", ProviderName: awsProvider, Actions: []string{"create"},
			After: map[string]any{}},
		{Address: "aws_iam_role.free", Type: "aws_iam_role", ProviderName: awsProvider, Actions: []string{"create"},
			After: map[string]any{}},
		{Address: "azurerm_linux_virtual_machine.vm", Type: "azurerm_linux_virtual_machine", ProviderName: azureProvider, Actions: []string{"create"},
			After: map[string]any{"size": "Standard_B2s", "location": "eastus"}},
		{Address: "random_id.ignored", Type: "random_id", ProviderName: "registry.opentofu.org/hashicorp/random", Actions: []string{"create"},
			After: map[string]any{}},
	}
	got := estimator.Estimate(context.Background(), resources)

	wantBefore := 0.01*HoursPerMonth + 0.8 + 0.045*HoursPerMonth
	wantAfter := 0.01*HoursPerMonth + 0.08*HoursPerMonth + 0.8
	if !near(round(got.Before), round(wantBefore)) || !near(round(got.After), round(wantAfter)) {
		t.Fatalf("totals: got %v → %v, want %v → %v", got.Before, got.After, wantBefore, wantAfter)
	}
	var addresses []string
	for _, line := range got.Resources {
		addresses = append(addresses, line.Address)
	}
	// Largest changes first; the unchanged volume isn't listed.
	if strings.Join(addresses, ",") != "aws_instance.resized,aws_nat_gateway.gone,aws_instance.new" {
		t.Fatalf("lines: %v", addresses)
	}
	reasons := map[string]string{}
	for _, item := range got.NotPriced {
		reasons[item.Address] = item.Reason
	}
	for address, want := range map[string]string{
		"aws_instance.unknown":             "isn't known until apply",
		"aws_lambda_function.usage":        "Usage-based",
		"azurerm_linux_virtual_machine.vm": "Azure prices are unavailable: testing",
	} {
		if !strings.Contains(reasons[address], want) {
			t.Errorf("%s: reason %q doesn't contain %q", address, reasons[address], want)
		}
	}
	if len(got.NotPriced) != 3 {
		t.Errorf("unexpected not priced resources: %+v", got.NotPriced)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "Azure prices are unavailable") {
		t.Errorf("warnings: %v", got.Warnings)
	}
}

func TestDescribe_notPriced(t *testing.T) {
	tests := map[string]struct {
		cloud, resourceType string
		values              map[string]any
		want                string
	}{
		"on-demand table":  {cloudAWS, "aws_dynamodb_table", map[string]any{"billing_mode": "PAY_PER_REQUEST"}, "Usage-based"},
		"spot node group":  {cloudAWS, "aws_eks_node_group", map[string]any{"capacity_type": "SPOT"}, "Spot"},
		"Oracle":           {cloudAWS, "aws_db_instance", map[string]any{"instance_class": "db.t3.micro", "engine": "oracle-se2"}, "oracle-se2"},
		"unknown type":     {cloudAWS, "aws_something_new", map[string]any{}, "Not supported"},
		"Ultra disk":       {cloudAzure, "azurerm_managed_disk", map[string]any{"storage_account_type": "UltraSSD_LRS", "disk_size_gb": 10.0, "location": "eastus"}, "UltraSSD_LRS"},
		"vCore SQL":        {cloudAzure, "azurerm_mssql_database", map[string]any{"sku_name": "GP_Gen5_2", "location": "eastus"}, "DTU"},
		"consumption plan": {cloudAzure, "azurerm_service_plan", map[string]any{"sku_name": "Y1", "location": "eastus"}, "Usage-based"},
		"elastic premium":  {cloudAzure, "azurerm_service_plan", map[string]any{"sku_name": "EP1", "location": "eastus"}, "Elastic Premium"},
		"storage account":  {cloudAzure, "azurerm_storage_account", map[string]any{}, "Usage-based"},
		"VM without size":  {cloudAzure, "azurerm_linux_virtual_machine", map[string]any{"location": "eastus"}, "isn't known"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := describe(test.cloud, test.resourceType, test.values, "us-east-1")
			if got.kind != notPriced || !strings.Contains(got.reason, test.want) {
				t.Fatalf("got %+v, want a reason containing %q", got, test.want)
			}
		})
	}
	if got := describe(cloudAzure, "azurerm_resource_group", map[string]any{}, ""); got.kind != free {
		t.Fatalf("resource groups should be free: %+v", got)
	}
	if got := describe(cloudAWS, "aws_route_table_association", map[string]any{}, ""); got.kind != free {
		t.Fatalf("route table associations should be free: %+v", got)
	}
}

func TestAWSRegion(t *testing.T) {
	if got := awsRegion(map[string]any{"region": "eu-west-1"}, "us-east-1"); got != "eu-west-1" {
		t.Errorf("region attribute: %s", got)
	}
	if got := awsRegion(map[string]any{"arn": "arn:aws:ec2:ap-south-1:123:instance/i-1"}, "us-east-1"); got != "ap-south-1" {
		t.Errorf("ARN: %s", got)
	}
	if got := awsRegion(map[string]any{}, "us-west-2"); got != "us-west-2" {
		t.Errorf("fallback: %s", got)
	}
}

func TestDiskTier(t *testing.T) {
	for _, test := range []struct {
		letter string
		size   float64
		want   string
	}{{"P", 100, "P10"}, {"P", 128, "P10"}, {"P", 129, "P15"}, {"E", 4, "E1"}, {"S", 30, "S4"}, {"S", 32767, "S80"}, {"P", 40000, ""}} {
		if got := diskTier(test.letter, test.size); got != test.want {
			t.Errorf("%s %g: got %q, want %q", test.letter, test.size, got, test.want)
		}
	}
}

func TestAppServiceSku(t *testing.T) {
	for in, want := range map[string]string{"P1v3": "P1 v3", "P0v3": "P0 v3", "P1mv3": "P1 mv3", "B1": "B1", "S2": "S2", "I1v2": "I1 v2"} {
		if got := appServiceSku(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
	}
}

func TestFormatMoney(t *testing.T) {
	for in, want := range map[float64]string{0: "$0.00", 1.006: "$1.01", 1234.5: "$1,234.50", 1234567.891: "$1,234,567.89", -12.3: "-$12.30"} {
		if got := FormatMoney(in); got != want {
			t.Errorf("%v: got %q, want %q", in, got, want)
		}
	}
	if got := FormatDelta(5); got != "+$5.00" {
		t.Errorf("delta: %s", got)
	}
	if got := FormatDelta(-5); got != "-$5.00" {
		t.Errorf("delta: %s", got)
	}
}

func TestCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "prices.json")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := OpenCache(path)
	cache.now = func() time.Time { return now }
	cache.Put("a", 1.5)
	cache.Save()

	reopened := OpenCache(path)
	reopened.now = func() time.Time { return now.Add(time.Hour) }
	if price, ok := reopened.Get("a"); !ok || price != 1.5 {
		t.Fatalf("got %v %v", price, ok)
	}
	reopened.now = func() time.Time { return now.Add(CacheTTL + time.Hour) }
	if _, ok := reopened.Get("a"); ok {
		t.Fatal("expired price was used")
	}
	var nilCache *Cache
	nilCache.Put("a", 1)
	if _, ok := nilCache.Get("a"); ok {
		t.Fatal("nil cache returned a price")
	}
}
