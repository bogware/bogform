// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package cost

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type kind int

const (
	// free resources have no charge of their own, so they're left out of the estimate silently.
	free kind = iota
	priceable
	notPriced
)

// part is one priced element of a resource: a unit price times a monthly quantity.
type part struct {
	aws      *AWSQuery
	azure    *AzureQuery
	quantity float64
}

type spec struct {
	kind        kind
	description string
	parts       []part
	reason      string
}

func usageBased(what string) spec {
	return spec{kind: notPriced, reason: "Usage-based: " + what + "."}
}

func unknownValue(attribute string) spec {
	return spec{kind: notPriced, reason: fmt.Sprintf("Its %s isn't known until apply.", attribute)}
}

var unsupported = spec{kind: notPriced, reason: "Not supported yet."}

func describe(cloud, resourceType string, values map[string]any, defaultRegion string) spec {
	if cloud == cloudAWS {
		return describeAWS(resourceType, values, awsRegion(values, defaultRegion))
	}
	return describeAzure(resourceType, values)
}

// Value helpers. Each returns the zero value when an attribute is missing, null, or unknown.

func str(values map[string]any, name string) string {
	s, _ := values[name].(string)
	return s
}

func num(values map[string]any, name string) (float64, bool) {
	n, ok := values[name].(float64)
	return n, ok
}

func first(values map[string]any, name string) map[string]any {
	if list, ok := values[name].([]any); ok && len(list) > 0 {
		block, _ := list[0].(map[string]any)
		return block
	}
	return nil
}

func plural(count float64, singular, pluralForm string) string {
	if count == 1 {
		return "1 " + singular
	}
	return strconv.FormatFloat(count, 'f', -1, 64) + " " + pluralForm
}

// AWS

var arnRegion = regexp.MustCompile(`^arn:[^:]+:[^:]+:([a-z0-9-]+):`)

func awsRegion(values map[string]any, fallback string) string {
	if region := str(values, "region"); region != "" {
		return region
	}
	if match := arnRegion.FindStringSubmatch(str(values, "arn")); match != nil {
		return match[1]
	}
	return fallback
}

// regionPrefix matches the region code that starts most AWS usage types outside us-east-1, such as
// "USW2-" or "EU-".
const regionPrefix = `^(?:[A-Z]{2,4}[0-9]?-)?`

func term(field, value string) AWSFilter { return AWSFilter{Field: field, Value: value} }

func contains(field, value string) AWSFilter {
	return AWSFilter{Field: field, Value: value, Contains: true}
}

func ec2Instance(region, instanceType, tenancy string) *AWSQuery {
	return &AWSQuery{
		Service: "AmazonEC2",
		Filters: []AWSFilter{
			term("instanceType", instanceType), term("regionCode", region), term("operatingSystem", "Linux"),
			term("tenancy", tenancy), term("preInstalledSw", "NA"), term("capacitystatus", "Used"),
			term("licenseModel", "No License required"),
		},
		UsageType: regionPrefix + `(BoxUsage|DedicatedUsage):`,
		Unit:      "Hrs",
	}
}

func ebsVolume(region, volumeType string) *AWSQuery {
	return &AWSQuery{
		Service:   "AmazonEC2",
		Filters:   []AWSFilter{term("productFamily", "Storage"), term("regionCode", region), term("volumeApiName", volumeType)},
		UsageType: regionPrefix + `EBS:VolumeUsage`,
		Unit:      "GB-Mo",
	}
}

var rdsEngines = map[string]string{
	"postgres":          "PostgreSQL",
	"mysql":             "MySQL",
	"mariadb":           "MariaDB",
	"aurora-postgresql": "Aurora PostgreSQL",
	"aurora-mysql":      "Aurora MySQL",
}

var rdsStorage = map[string]string{
	"gp2":      `GP2-Storage$`,
	"gp3":      `GP3-Storage$`,
	"io1":      `PIOPS-Storage$`,
	"io2":      `PIOPS-Storage-IO2$`,
	"standard": `StorageUsage$`,
}

var loadBalancerFamilies = map[string]string{
	"application": "Load Balancer-Application",
	"network":     "Load Balancer-Network",
	"gateway":     "Load Balancer-Gateway",
}

var cacheEngines = map[string]string{"redis": "Redis", "memcached": "Memcached", "valkey": "Valkey"}

var awsUsageBased = map[string]string{
	"aws_lambda_function":                  "requests and duration",
	"aws_s3_bucket":                        "storage and requests",
	"aws_cloudfront_distribution":          "requests and data transfer",
	"aws_apigatewayv2_api":                 "requests",
	"aws_api_gateway_rest_api":             "requests",
	"aws_sqs_queue":                        "requests",
	"aws_sns_topic":                        "messages",
	"aws_cloudwatch_log_group":             "ingestion and storage",
	"aws_efs_file_system":                  "storage",
	"aws_ecr_repository":                   "storage",
	"aws_sfn_state_machine":                "state transitions",
	"aws_kinesis_firehose_delivery_stream": "data ingested",
	"aws_elasticache_serverless_cache":     "data stored and compute used",
	"aws_rds_cluster":                      "storage and I/O; its instances are priced separately",
	"aws_sesv2_email_identity":             "messages sent",
	"aws_ses_domain_identity":              "messages sent",
	"aws_cloudwatch_event_rule":            "events",
	"aws_secretsmanager_secret":            "secrets and API calls",
	"aws_kms_key":                          "keys and requests",
	"aws_route53_zone":                     "hosted zones and queries",
}

// awsFree lists resource types, or prefixes ending in "_", with no charge of their own.
var awsFree = []string{
	"aws_iam_", "aws_s3_bucket_", "aws_security_group", "aws_security_group_rule", "aws_vpc_security_group_",
	"aws_vpc", "aws_subnet", "aws_route_table", "aws_route_table_association", "aws_main_route_table_association",
	"aws_route", "aws_internet_gateway", "aws_route53_record", "aws_acm_certificate", "aws_acm_certificate_validation",
	"aws_lb_listener_rule", "aws_lb_target_group_attachment", "aws_lambda_permission", "aws_lambda_alias", "aws_cloudfront_origin_access_",
	"aws_cloudfront_response_headers_policy", "aws_cloudfront_cache_policy", "aws_cloudfront_origin_request_policy",
	"aws_apigatewayv2_route", "aws_apigatewayv2_integration", "aws_apigatewayv2_stage", "aws_apigatewayv2_deployment",
	"aws_cloudwatch_log_resource_policy", "aws_cloudwatch_event_target", "aws_sesv2_email_identity_",
	"aws_default_", "aws_key_pair", "aws_ecr_lifecycle_policy", "aws_sns_topic_subscription", "aws_sns_topic_policy",
	"aws_sqs_queue_policy", "aws_lb_listener", "aws_lb_target_group", "aws_db_subnet_group", "aws_db_parameter_group",
	"aws_rds_cluster_parameter_group", "aws_elasticache_subnet_group", "aws_elasticache_parameter_group",
	"aws_network_acl", "aws_eip_association", "aws_lambda_event_source_mapping", "aws_ssm_parameter",
	"aws_dynamodb_table_item", "aws_cloudwatch_log_subscription_filter", "aws_lambda_function_url",
}

func isFree(resourceType string, list []string) bool {
	for _, entry := range list {
		if resourceType == entry || (strings.HasSuffix(entry, "_") && strings.HasPrefix(resourceType, entry)) {
			return true
		}
	}
	return false
}

func describeAWS(resourceType string, v map[string]any, region string) spec {
	switch resourceType {
	case "aws_instance":
		instanceType := str(v, "instance_type")
		if instanceType == "" {
			return unknownValue("instance type")
		}
		tenancy := "Shared"
		switch str(v, "tenancy") {
		case "dedicated":
			tenancy = "Dedicated"
		case "host":
			return spec{kind: notPriced, reason: "Dedicated hosts aren't supported yet."}
		}
		if str(v, "instance_lifecycle") == "spot" || first(v, "instance_market_options") != nil {
			return spec{kind: notPriced, reason: "Spot prices vary."}
		}
		s := spec{kind: priceable, description: instanceType + " (Linux)", parts: []part{{aws: ec2Instance(region, instanceType, tenancy), quantity: HoursPerMonth}}}
		if root := first(v, "root_block_device"); root != nil {
			size, sized := num(root, "volume_size")
			if volumeType := str(root, "volume_type"); sized && volumeType != "" {
				s.parts = append(s.parts, part{aws: ebsVolume(region, volumeType), quantity: size})
				s.description += fmt.Sprintf(", %g GB %s root volume", size, volumeType)
			}
		}
		return s
	case "aws_ebs_volume":
		volumeType := str(v, "type")
		if volumeType == "" {
			return unknownValue("volume type")
		}
		size, ok := num(v, "size")
		if !ok {
			return unknownValue("size")
		}
		s := spec{kind: priceable, description: fmt.Sprintf("%g GB %s", size, volumeType), parts: []part{{aws: ebsVolume(region, volumeType), quantity: size}}}
		if iops, ok := num(v, "iops"); ok && ((volumeType == "gp3" && iops > 3000) || volumeType == "io1" || volumeType == "io2") {
			s.description += ", provisioned IOPS not included"
		}
		return s
	case "aws_db_instance", "aws_rds_cluster_instance":
		class := str(v, "instance_class")
		if class == "" {
			return unknownValue("instance class")
		}
		engine := str(v, "engine")
		name, ok := rdsEngines[engine]
		if !ok {
			if engine == "" {
				return unknownValue("engine")
			}
			return spec{kind: notPriced, reason: fmt.Sprintf("The %s engine isn't supported yet.", engine)}
		}
		deployment := "Single-AZ"
		if multiAZ, _ := v["multi_az"].(bool); multiAZ {
			deployment = "Multi-AZ"
		}
		instance := &AWSQuery{
			Service:   "AmazonRDS",
			Filters:   []AWSFilter{term("instanceType", class), term("regionCode", region), term("databaseEngine", name), term("deploymentOption", deployment)},
			UsageType: regionPrefix + `(InstanceUsage|Multi-AZUsage):`,
			Unit:      "Hrs",
		}
		s := spec{kind: priceable, description: fmt.Sprintf("%s %s, %s", class, name, deployment), parts: []part{{aws: instance, quantity: HoursPerMonth}}}
		if resourceType == "aws_db_instance" {
			size, sized := num(v, "allocated_storage")
			storageType := str(v, "storage_type")
			if pattern, known := rdsStorage[storageType]; sized && known {
				storage := &AWSQuery{
					Service:   "AmazonRDS",
					Filters:   []AWSFilter{term("productFamily", "Database Storage"), term("regionCode", region), term("databaseEngine", name), term("deploymentOption", deployment)},
					UsageType: pattern,
					Unit:      "GB-Mo",
				}
				s.parts = append(s.parts, part{aws: storage, quantity: size})
				s.description += fmt.Sprintf(", %g GB %s", size, storageType)
			}
		}
		return s
	case "aws_nat_gateway":
		return spec{kind: priceable, description: "NAT gateway, data processing not included", parts: []part{{aws: &AWSQuery{
			Service:   "AmazonEC2",
			Filters:   []AWSFilter{term("productFamily", "NAT Gateway"), term("regionCode", region), contains("usagetype", "NatGateway-Hours")},
			UsageType: regionPrefix + `NatGateway-Hours$`,
			Unit:      "Hrs",
		}, quantity: HoursPerMonth}}}
	case "aws_lb", "aws_alb", "aws_elb":
		family, lbType := "Load Balancer", "classic"
		if resourceType != "aws_elb" {
			lbType = str(v, "load_balancer_type")
			if lbType == "" {
				lbType = "application"
			}
			var ok bool
			if family, ok = loadBalancerFamilies[lbType]; !ok {
				return unsupported
			}
		}
		return spec{kind: priceable, description: lbType + " load balancer, capacity units not included", parts: []part{{aws: &AWSQuery{
			Service:      "AWSELB",
			Filters:      []AWSFilter{term("productFamily", family), term("regionCode", region)},
			UsageType:    regionPrefix + `LoadBalancerUsage$`,
			UsageExclude: `(^|-)(TS|Outposts)-`,
			Unit:         "Hrs",
		}, quantity: HoursPerMonth}}}
	case "aws_dynamodb_table":
		if str(v, "billing_mode") == "PAY_PER_REQUEST" {
			return usageBased("on-demand requests")
		}
		read, readKnown := num(v, "read_capacity")
		write, writeKnown := num(v, "write_capacity")
		if !readKnown || !writeKnown {
			return unknownValue("capacity")
		}
		for _, index := range blocksOf(v, "global_secondary_index") {
			r, _ := num(index, "read_capacity")
			w, _ := num(index, "write_capacity")
			read, write = read+r, write+w
		}
		prefix := ""
		if str(v, "table_class") == "STANDARD_INFREQUENT_ACCESS" {
			prefix = "IA-"
		}
		capacity := func(unit string) *AWSQuery {
			q := &AWSQuery{
				Service:   "AmazonDynamoDB",
				Filters:   []AWSFilter{term("productFamily", "Provisioned IOPS"), term("regionCode", region)},
				UsageType: regionPrefix + prefix + unit + `-Hrs$`,
				Unit:      unit + "-Hrs",
				Tiered:    true,
			}
			if prefix == "" {
				q.UsageExclude = `(^|-)IA-`
			}
			return q
		}
		return spec{kind: priceable, description: fmt.Sprintf("%g read and %g write capacity units, storage not included", read, write), parts: []part{
			{aws: capacity("ReadCapacityUnit"), quantity: read * HoursPerMonth},
			{aws: capacity("WriteCapacityUnit"), quantity: write * HoursPerMonth},
		}}
	case "aws_elasticache_cluster", "aws_elasticache_replication_group":
		nodeType := str(v, "node_type")
		if nodeType == "" {
			return unknownValue("node type")
		}
		engine := str(v, "engine")
		if engine == "" {
			engine = "redis"
		}
		engineName, ok := cacheEngines[engine]
		if !ok {
			return unsupported
		}
		var nodes float64
		if resourceType == "aws_elasticache_cluster" {
			nodes, ok = num(v, "num_cache_nodes")
		} else if clusters, has := num(v, "num_cache_clusters"); has {
			nodes, ok = clusters, true
		} else if groups, has := num(v, "num_node_groups"); has {
			replicas, _ := num(v, "replicas_per_node_group")
			nodes, ok = groups*(replicas+1), true
		}
		if !ok {
			return unknownValue("node count")
		}
		return spec{kind: priceable, description: fmt.Sprintf("%s %s, %s", nodeType, engineName, plural(nodes, "node", "nodes")), parts: []part{{aws: &AWSQuery{
			Service:   "AmazonElastiCache",
			Filters:   []AWSFilter{term("instanceType", nodeType), term("regionCode", region), term("cacheEngine", engineName)},
			UsageType: regionPrefix + `NodeUsage:`,
			Unit:      "Hrs",
		}, quantity: nodes * HoursPerMonth}}}
	case "aws_eks_cluster":
		return spec{kind: priceable, description: "EKS control plane, standard support", parts: []part{{aws: &AWSQuery{
			Service:   "AmazonEKS",
			Filters:   []AWSFilter{term("regionCode", region), contains("usagetype", "AmazonEKS-Hours:perCluster")},
			UsageType: `AmazonEKS-Hours:perCluster$`,
		}, quantity: HoursPerMonth}}}
	case "aws_eks_node_group":
		if str(v, "capacity_type") == "SPOT" {
			return spec{kind: notPriced, reason: "Spot prices vary."}
		}
		types, _ := v["instance_types"].([]any)
		instanceType := ""
		if len(types) > 0 {
			instanceType, _ = types[0].(string)
		}
		if instanceType == "" {
			return unknownValue("instance type")
		}
		desired, ok := num(first(v, "scaling_config"), "desired_size")
		if !ok {
			return unknownValue("desired size")
		}
		return spec{kind: priceable, description: fmt.Sprintf("%s (Linux), %s", instanceType, plural(desired, "node", "nodes")), parts: []part{{aws: ec2Instance(region, instanceType, "Shared"), quantity: desired * HoursPerMonth}}}
	}
	if what, ok := awsUsageBased[resourceType]; ok {
		return usageBased(what)
	}
	if isFree(resourceType, awsFree) {
		return spec{kind: free}
	}
	return unsupported
}

func blocksOf(values map[string]any, name string) []map[string]any {
	var result []map[string]any
	list, _ := values[name].([]any)
	for _, item := range list {
		if block, ok := item.(map[string]any); ok {
			result = append(result, block)
		}
	}
	return result
}

// Azure

func azureLocation(values map[string]any) string {
	return strings.ToLower(strings.ReplaceAll(str(values, "location"), " ", ""))
}

func azureVM(location, size string, windows bool) *AzureQuery {
	q := &AzureQuery{
		Filter:       fmt.Sprintf("serviceName eq 'Virtual Machines' and armRegionName eq '%s' and armSkuName eq '%s' and priceType eq 'Consumption'", odata(location), odata(size)),
		SkuExcludes:  []string{"Spot", "Low Priority"},
		Unit:         "1 Hour",
		ProductMatch: []string{},
	}
	if windows {
		q.ProductMatch = []string{"Windows"}
	} else {
		q.ProductExcludes = []string{"Windows"}
	}
	return q
}

// odata escapes a value for a quoted OData string.
func odata(value string) string { return strings.ReplaceAll(value, "'", "''") }

var diskTiers = []struct {
	size  float64
	names map[string]string
}{
	{4, map[string]string{"P": "P1", "E": "E1", "S": "S4"}},
	{8, map[string]string{"P": "P2", "E": "E2", "S": "S4"}},
	{16, map[string]string{"P": "P3", "E": "E3", "S": "S4"}},
	{32, map[string]string{"P": "P4", "E": "E4", "S": "S4"}},
	{64, map[string]string{"P": "P6", "E": "E6", "S": "S6"}},
	{128, map[string]string{"P": "P10", "E": "E10", "S": "S10"}},
	{256, map[string]string{"P": "P15", "E": "E15", "S": "S15"}},
	{512, map[string]string{"P": "P20", "E": "E20", "S": "S20"}},
	{1024, map[string]string{"P": "P30", "E": "E30", "S": "S30"}},
	{2048, map[string]string{"P": "P40", "E": "E40", "S": "S40"}},
	{4096, map[string]string{"P": "P50", "E": "E50", "S": "S50"}},
	{8192, map[string]string{"P": "P60", "E": "E60", "S": "S60"}},
	{16384, map[string]string{"P": "P70", "E": "E70", "S": "S70"}},
	{32767, map[string]string{"P": "P80", "E": "E80", "S": "S80"}},
}

var diskKinds = map[string]struct{ letter, redundancy, product string }{
	"Premium_LRS":     {"P", "LRS", "Premium SSD Managed Disks"},
	"Premium_ZRS":     {"P", "ZRS", "Premium SSD Managed Disks"},
	"StandardSSD_LRS": {"E", "LRS", "Standard SSD Managed Disks"},
	"StandardSSD_ZRS": {"E", "ZRS", "Standard SSD Managed Disks"},
	"Standard_LRS":    {"S", "LRS", "Standard HDD Managed Disks"},
}

// diskTier returns the billing tier, such as "P10", for a disk's size in GB.
func diskTier(letter string, size float64) string {
	for _, tier := range diskTiers {
		if size <= tier.size {
			return tier.names[letter]
		}
	}
	return ""
}

var planVersion = regexp.MustCompile(`^([A-Z]+[0-9]+)(m?v[0-9]+)$`)

// appServiceSku converts a plan's sku_name, such as "P1v3", to the price list's form, "P1 v3".
func appServiceSku(name string) string {
	if match := planVersion.FindStringSubmatch(name); match != nil {
		return match[1] + " " + match[2]
	}
	return name
}

var dtuSku = regexp.MustCompile(`^(Basic|S[0-9]+|P[0-9]+)$`)

var postgresSize = regexp.MustCompile(`^([A-Z])([0-9]+)([a-z]*)_(v[0-9]+)$`)

var azureUsageBased = map[string]string{
	"azurerm_storage_account":         "storage, transactions and data transfer",
	"azurerm_linux_function_app":      "executions, unless it's on a dedicated plan, which is priced separately",
	"azurerm_windows_function_app":    "executions, unless it's on a dedicated plan, which is priced separately",
	"azurerm_cosmosdb_account":        "throughput and storage",
	"azurerm_container_app":           "vCPU and memory used",
	"azurerm_log_analytics_workspace": "data ingested",
	"azurerm_key_vault":               "operations",
	"azurerm_application_insights":    "data ingested",
	"azurerm_dns_zone":                "zones and queries",
}

var azureFree = []string{
	"azurerm_resource_group", "azurerm_virtual_network", "azurerm_subnet", "azurerm_network_security_",
	"azurerm_role_assignment", "azurerm_role_definition", "azurerm_user_assigned_identity",
	"azurerm_federated_identity_credential", "azurerm_dns_a_record", "azurerm_dns_aaaa_record",
	"azurerm_dns_cname_record", "azurerm_dns_mx_record", "azurerm_dns_txt_record", "azurerm_network_interface",
	"azurerm_storage_container", "azurerm_linux_web_app", "azurerm_windows_web_app", "azurerm_mssql_server",
	"azurerm_mssql_firewall_rule", "azurerm_postgresql_flexible_server_firewall_rule",
	"azurerm_postgresql_flexible_server_database", "azurerm_postgresql_flexible_server_configuration",
	"azurerm_subnet_network_security_group_association", "azurerm_key_vault_secret", "azurerm_key_vault_access_policy",
	"azurerm_monitor_diagnostic_setting", "azurerm_management_lock",
}

func describeAzure(resourceType string, v map[string]any) spec {
	location := azureLocation(v)
	switch resourceType {
	case "azurerm_linux_virtual_machine", "azurerm_windows_virtual_machine", "azurerm_virtual_machine":
		size := str(v, "size")
		if resourceType == "azurerm_virtual_machine" {
			size = str(v, "vm_size")
		}
		if size == "" || location == "" {
			return unknownValue("size or location")
		}
		if str(v, "priority") == "Spot" {
			return spec{kind: notPriced, reason: "Spot prices vary."}
		}
		windows := resourceType == "azurerm_windows_virtual_machine"
		os := "Linux"
		if windows {
			os = "Windows"
		}
		return spec{kind: priceable, description: fmt.Sprintf("%s (%s), disks not included", size, os), parts: []part{{azure: azureVM(location, size, windows), quantity: HoursPerMonth}}}
	case "azurerm_linux_virtual_machine_scale_set", "azurerm_windows_virtual_machine_scale_set":
		size := str(v, "sku")
		instances, ok := num(v, "instances")
		if size == "" || location == "" || !ok {
			return unknownValue("size, location or instance count")
		}
		windows := resourceType == "azurerm_windows_virtual_machine_scale_set"
		return spec{kind: priceable, description: fmt.Sprintf("%s, %s", size, plural(instances, "instance", "instances")), parts: []part{{azure: azureVM(location, size, windows), quantity: instances * HoursPerMonth}}}
	case "azurerm_managed_disk":
		storageType := str(v, "storage_account_type")
		size, sized := num(v, "disk_size_gb")
		if storageType == "" || !sized || location == "" {
			return unknownValue("type, size or location")
		}
		disk, ok := diskKinds[storageType]
		if !ok {
			return spec{kind: notPriced, reason: fmt.Sprintf("%s disks are priced by capacity and performance, which isn't supported yet.", storageType)}
		}
		tier := diskTier(disk.letter, size)
		if tier == "" {
			return unsupported
		}
		sku := tier + " " + disk.redundancy
		return spec{kind: priceable, description: fmt.Sprintf("%s disk (%g GB), transactions not included", sku, size), parts: []part{{azure: &AzureQuery{
			Filter:       fmt.Sprintf("serviceName eq 'Storage' and armRegionName eq '%s' and skuName eq '%s' and priceType eq 'Consumption'", odata(location), odata(sku)),
			ProductMatch: []string{disk.product},
			MeterName:    sku + " Disk",
			Unit:         "1/Month",
		}, quantity: 1}}}
	case "azurerm_service_plan":
		name := str(v, "sku_name")
		if name == "" || location == "" {
			return unknownValue("SKU or location")
		}
		switch {
		case name == "F1" || name == "D1" || name == "Free":
			return spec{kind: free}
		case name == "Y1" || name == "FC1":
			return usageBased("executions")
		case strings.HasPrefix(name, "EP"):
			return spec{kind: notPriced, reason: "Elastic Premium plans aren't supported yet."}
		}
		workers, ok := num(v, "worker_count")
		if !ok {
			workers = 1
		}
		q := &AzureQuery{
			Filter: fmt.Sprintf("serviceName eq 'Azure App Service' and armRegionName eq '%s' and skuName eq '%s' and priceType eq 'Consumption'", odata(location), odata(appServiceSku(name))),
			Unit:   "1 Hour",
		}
		os := str(v, "os_type")
		if os == "Windows" {
			q.ProductExcludes = []string{"Linux"}
		} else {
			q.ProductMatch = []string{"Linux"}
			os = "Linux"
		}
		return spec{kind: priceable, description: fmt.Sprintf("%s (%s), %s", name, os, plural(workers, "worker", "workers")), parts: []part{{azure: q, quantity: workers * HoursPerMonth}}}
	case "azurerm_mssql_database":
		sku := str(v, "sku_name")
		if sku == "" || location == "" {
			return unknownValue("SKU or location")
		}
		if !dtuSku.MatchString(sku) {
			return spec{kind: notPriced, reason: fmt.Sprintf("The %s SKU isn't supported yet; only DTU SKUs such as S0 are.", sku)}
		}
		skuName, meter := sku, sku+" DTUs"
		if sku == "Basic" {
			skuName, meter = "B", "B DTU"
		}
		return spec{kind: priceable, description: sku + " database, storage beyond the included amount not included", parts: []part{{azure: &AzureQuery{
			Filter:    fmt.Sprintf("serviceName eq 'SQL Database' and armRegionName eq '%s' and skuName eq '%s' and priceType eq 'Consumption'", odata(location), odata(skuName)),
			MeterName: meter,
			Unit:      "1/Day",
		}, quantity: HoursPerMonth / 24}}}
	case "azurerm_postgresql_flexible_server":
		sku := str(v, "sku_name")
		if sku == "" || location == "" {
			return unknownValue("SKU or location")
		}
		tier, size, ok := strings.Cut(sku, "_Standard_")
		if !ok {
			return unsupported
		}
		if tier == "B" {
			return spec{kind: priceable, description: size + " (Burstable), storage not included", parts: []part{{azure: &AzureQuery{
				Filter:       fmt.Sprintf("serviceName eq 'Azure Database for PostgreSQL' and armRegionName eq '%s' and skuName eq '%s' and priceType eq 'Consumption'", odata(location), odata(strings.ToUpper(size))),
				ProductMatch: []string{"Flexible Server Burstable"},
				Unit:         "1 Hour",
			}, quantity: HoursPerMonth}}}
		}
		match := postgresSize.FindStringSubmatch(size)
		family := map[string]string{"GP": "General Purpose", "MO": "Memory Optimized"}[tier]
		if match == nil || family == "" {
			return unsupported
		}
		vcores, _ := strconv.ParseFloat(match[2], 64)
		series := match[1] + match[3] + match[4]
		return spec{kind: priceable, description: fmt.Sprintf("%s (%s, %g vCores), storage not included", size, family, vcores), parts: []part{{azure: &AzureQuery{
			Filter:       fmt.Sprintf("serviceName eq 'Azure Database for PostgreSQL' and armRegionName eq '%s' and skuName eq '1 vCore' and meterName eq 'vCore' and priceType eq 'Consumption'", odata(location)),
			ProductMatch: []string{"Flexible Server " + family + " " + series + " "},
			Unit:         "1 Hour",
		}, quantity: vcores * HoursPerMonth}}}
	case "azurerm_kubernetes_cluster":
		if location == "" {
			return unknownValue("location")
		}
		s := spec{kind: priceable, description: "AKS"}
		switch tier := str(v, "sku_tier"); tier {
		case "", "Free":
			s.description += " (Free tier)"
		case "Standard", "Premium":
			s.description += " (" + tier + " tier)"
			meters := []string{"Standard Uptime SLA"}
			if tier == "Premium" {
				meters = append(meters, "Standard Long Term Support")
			}
			for _, meter := range meters {
				s.parts = append(s.parts, part{azure: &AzureQuery{
					Filter:    fmt.Sprintf("serviceName eq 'Azure Kubernetes Service' and armRegionName eq '%s' and skuName eq 'Standard' and priceType eq 'Consumption'", odata(location)),
					MeterName: meter,
					Unit:      "1 Hour",
				}, quantity: HoursPerMonth})
			}
		default:
			return unsupported
		}
		pool := first(v, "default_node_pool")
		size := str(pool, "vm_size")
		count, ok := num(pool, "node_count")
		if !ok {
			count, ok = num(pool, "min_count")
		}
		if size == "" || !ok {
			return unknownValue("default node pool's size or node count")
		}
		s.parts = append(s.parts, part{azure: azureVM(location, size, false), quantity: count * HoursPerMonth})
		s.description += fmt.Sprintf(", default pool %s × %g", size, count)
		return s
	case "azurerm_kubernetes_cluster_node_pool":
		size := str(v, "vm_size")
		count, ok := num(v, "node_count")
		if !ok {
			count, ok = num(v, "min_count")
		}
		if size == "" || !ok {
			return unknownValue("size or node count")
		}
		if str(v, "priority") == "Spot" {
			return spec{kind: notPriced, reason: "Spot prices vary."}
		}
		// Node pools don't have their own location, and are priced in the cluster's.
		if location == "" {
			return spec{kind: notPriced, reason: "Node pools are priced in their cluster's region, which isn't in their values."}
		}
		windows := str(v, "os_type") == "Windows"
		return spec{kind: priceable, description: fmt.Sprintf("%s, %s", size, plural(count, "node", "nodes")), parts: []part{{azure: azureVM(location, size, windows), quantity: count * HoursPerMonth}}}
	}
	if what, ok := azureUsageBased[resourceType]; ok {
		return usageBased(what)
	}
	if isFree(resourceType, azureFree) {
		return spec{kind: free}
	}
	return unsupported
}
