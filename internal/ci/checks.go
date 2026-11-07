// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package ci

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// SkipChecksTag is the tag that turns checks off for one resource. Its value is a comma-separated list
// of check IDs, or "all".
const SkipChecksTag = "bogform:skip-checks"

// Finding is a check that a planned resource doesn't pass. It names the resource and the problem, but
// never includes attribute values.
type Finding struct {
	Check   string `json:"check"`
	Address string `json:"address"`
	Message string `json:"message"`
}

// Check IDs. They're stable, so that people can turn individual checks off.
const (
	CheckS3PublicAccessBlock  = "aws-s3-public-access-block"
	CheckS3PublicACL          = "aws-s3-public-acl"
	CheckS3PublicPolicy       = "aws-s3-public-policy"
	CheckEBSUnencrypted       = "aws-ebs-unencrypted"
	CheckRDSUnencrypted       = "aws-rds-unencrypted"
	CheckEFSUnencrypted       = "aws-efs-unencrypted"
	CheckIAMWildcard          = "aws-iam-wildcard"
	CheckSecurityGroupAdmin   = "aws-security-group-open-admin"
	CheckNSGAdmin             = "azure-nsg-open-admin"
	CheckStoragePublicAccess  = "azure-storage-public-access"
	CheckStorageContainer     = "azure-storage-container-public"
	CheckStorageHTTPSOnly     = "azure-storage-https-only"
	CheckStorageMinTLS        = "azure-storage-min-tls"
	CheckDatabaseFirewallOpen = "azure-database-open-firewall"
)

// CheckIDs lists every check, for validating -skip-checks.
var CheckIDs = []string{
	CheckS3PublicAccessBlock, CheckS3PublicACL, CheckS3PublicPolicy,
	CheckEBSUnencrypted, CheckRDSUnencrypted, CheckEFSUnencrypted,
	CheckIAMWildcard, CheckSecurityGroupAdmin, CheckNSGAdmin,
	CheckStoragePublicAccess, CheckStorageContainer, CheckStorageHTTPSOnly, CheckStorageMinTLS,
	CheckDatabaseFirewallOpen,
}

// adminPorts are remote administration ports that shouldn't be open to the internet.
var adminPorts = []int{22, 3389}

// runChecks evaluates the built-in checks against the resources a plan creates or updates. Values that
// aren't known until apply never produce a finding.
func runChecks(resources []plannedResource, skip []string) []Finding {
	var findings []Finding
	for _, resource := range resources {
		if !resource.changed() || resource.After == nil {
			continue
		}
		skipped := skip
		if tag := tagValue(resource.After, SkipChecksTag); tag != "" {
			for _, id := range strings.Split(tag, ",") {
				skipped = append(skipped, strings.TrimSpace(id))
			}
		}
		if slices.Contains(skipped, "all") {
			continue
		}
		for _, finding := range checkResource(resource) {
			if !slices.Contains(skipped, finding.Check) {
				findings = append(findings, finding)
			}
		}
	}
	return findings
}

func checkResource(r plannedResource) []Finding {
	v := r.After
	var found []Finding
	add := func(id, message string) {
		found = append(found, Finding{Check: id, Address: r.Address, Message: message})
	}

	switch r.Type {
	// AWS
	case "aws_s3_bucket_public_access_block", "aws_s3_account_public_access_block":
		var off []string
		for _, setting := range []string{"block_public_acls", "block_public_policy", "ignore_public_acls", "restrict_public_buckets"} {
			if value, known := boolValue(v, setting); known && !value {
				off = append(off, setting)
			}
		}
		if len(off) > 0 {
			add(CheckS3PublicAccessBlock, "Public access protection is turned off: "+strings.Join(off, ", ")+".")
		}
	case "aws_s3_bucket_acl", "aws_s3_bucket":
		switch stringValue(v, "acl") {
		case "public-read", "public-read-write", "authenticated-read":
			add(CheckS3PublicACL, fmt.Sprintf("The bucket's ACL, %q, makes objects readable outside your account.", stringValue(v, "acl")))
		}
		if r.Type == "aws_s3_bucket" && publicPolicy(stringValue(v, "policy")) {
			add(CheckS3PublicPolicy, "The bucket policy allows anyone (Principal \"*\") without a condition.")
		}
	case "aws_s3_bucket_policy":
		if publicPolicy(stringValue(v, "policy")) {
			add(CheckS3PublicPolicy, "The bucket policy allows anyone (Principal \"*\") without a condition.")
		}
	case "aws_ebs_volume":
		if encrypted, known := boolValue(v, "encrypted"); known && !encrypted {
			add(CheckEBSUnencrypted, "The volume isn't encrypted.")
		}
	case "aws_instance", "aws_launch_template":
		var volumes []map[string]any
		volumes = append(volumes, blocks(v, "root_block_device")...)
		volumes = append(volumes, blocks(v, "ebs_block_device")...)
		for _, device := range blocks(v, "block_device_mappings") {
			volumes = append(volumes, blocks(device, "ebs")...)
		}
		for _, volume := range volumes {
			if encrypted, known := boolValue(volume, "encrypted"); known && !encrypted {
				add(CheckEBSUnencrypted, "A block device isn't encrypted.")
				break
			}
		}
	case "aws_db_instance", "aws_rds_cluster", "aws_docdb_cluster", "aws_neptune_cluster":
		// Aurora replicas inherit encryption from their cluster, so a missing value isn't a problem.
		if encrypted, known := boolValue(v, "storage_encrypted"); known && !encrypted && stringValue(v, "replicate_source_db") == "" {
			add(CheckRDSUnencrypted, "The database's storage isn't encrypted.")
		}
	case "aws_efs_file_system":
		if encrypted, known := boolValue(v, "encrypted"); known && !encrypted {
			add(CheckEFSUnencrypted, "The file system isn't encrypted.")
		}
	case "aws_iam_policy", "aws_iam_role_policy", "aws_iam_user_policy", "aws_iam_group_policy":
		if wildcardPolicy(stringValue(v, "policy")) {
			add(CheckIAMWildcard, "The policy allows every action (\"*\") on every resource (\"*\").")
		}
	case "aws_iam_role":
		for _, inline := range blocks(v, "inline_policy") {
			if wildcardPolicy(stringValue(inline, "policy")) {
				add(CheckIAMWildcard, "An inline policy allows every action (\"*\") on every resource (\"*\").")
				break
			}
		}
	case "aws_security_group":
		for _, rule := range blocks(v, "ingress") {
			if port, open := awsRuleOpen(rule, append(stringList(rule, "cidr_blocks"), stringList(rule, "ipv6_cidr_blocks")...)); open {
				add(CheckSecurityGroupAdmin, fmt.Sprintf("Port %d is open to the internet.", port))
				break
			}
		}
	case "aws_security_group_rule":
		if stringValue(v, "type") == "ingress" {
			if port, open := awsRuleOpen(v, append(stringList(v, "cidr_blocks"), stringList(v, "ipv6_cidr_blocks")...)); open {
				add(CheckSecurityGroupAdmin, fmt.Sprintf("Port %d is open to the internet.", port))
			}
		}
	case "aws_vpc_security_group_ingress_rule":
		rule := map[string]any{"from_port": v["from_port"], "to_port": v["to_port"], "protocol": v["ip_protocol"]}
		var sources []string
		for _, name := range []string{"cidr_ipv4", "cidr_ipv6"} {
			if s := stringValue(v, name); s != "" {
				sources = append(sources, s)
			}
		}
		if port, open := awsRuleOpen(rule, sources); open {
			add(CheckSecurityGroupAdmin, fmt.Sprintf("Port %d is open to the internet.", port))
		}

	// Azure
	case "azurerm_network_security_group":
		for _, rule := range blocks(v, "security_rule") {
			if port, open := azureRuleOpen(rule); open {
				add(CheckNSGAdmin, fmt.Sprintf("Port %d is open to the internet.", port))
				break
			}
		}
	case "azurerm_network_security_rule":
		if port, open := azureRuleOpen(v); open {
			add(CheckNSGAdmin, fmt.Sprintf("Port %d is open to the internet.", port))
		}
	case "azurerm_storage_account":
		if public, known := boolValue(v, "allow_nested_items_to_be_public"); known && public {
			add(CheckStoragePublicAccess, "The account allows containers to be made public. Set allow_nested_items_to_be_public = false.")
		}
		for _, name := range []string{"https_traffic_only_enabled", "enable_https_traffic_only"} {
			if httpsOnly, known := boolValue(v, name); known && !httpsOnly {
				add(CheckStorageHTTPSOnly, "The account accepts unencrypted HTTP.")
			}
		}
		switch stringValue(v, "min_tls_version") {
		case "TLS1_0", "TLS1_1":
			add(CheckStorageMinTLS, "The account accepts TLS versions older than 1.2.")
		}
	case "azurerm_storage_container":
		if access := stringValue(v, "container_access_type"); access == "blob" || access == "container" {
			add(CheckStorageContainer, fmt.Sprintf("The container's access type, %q, makes its blobs readable by anyone.", access))
		}
	case "azurerm_mssql_firewall_rule", "azurerm_sql_firewall_rule", "azurerm_postgresql_flexible_server_firewall_rule",
		"azurerm_postgresql_firewall_rule", "azurerm_mysql_flexible_server_firewall_rule", "azurerm_mariadb_firewall_rule":
		if stringValue(v, "start_ip_address") == "0.0.0.0" && stringValue(v, "end_ip_address") == "255.255.255.255" {
			add(CheckDatabaseFirewallOpen, "The firewall rule allows every IP address.")
		}
	}
	return found
}

func internetSource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "0.0.0.0/0", "::/0", "*", "internet", "any":
		return true
	}
	return false
}

// awsRuleOpen reports whether an ingress rule opens an administration port to the internet, and which.
func awsRuleOpen(rule map[string]any, sources []string) (int, bool) {
	if !slices.ContainsFunc(sources, internetSource) {
		return 0, false
	}
	protocol := strings.ToLower(stringValue(rule, "protocol"))
	if protocol != "-1" && protocol != "all" && protocol != "tcp" && protocol != "6" {
		return 0, false
	}
	from, fromKnown := numberValue(rule, "from_port")
	to, toKnown := numberValue(rule, "to_port")
	for _, port := range adminPorts {
		if protocol == "-1" || protocol == "all" {
			return port, true
		}
		if fromKnown && toKnown && float64(port) >= from && float64(port) <= to {
			return port, true
		}
	}
	return 0, false
}

// azureRuleOpen reports whether an NSG rule opens an administration port to the internet, and which.
func azureRuleOpen(rule map[string]any) (int, bool) {
	if !strings.EqualFold(stringValue(rule, "direction"), "Inbound") || !strings.EqualFold(stringValue(rule, "access"), "Allow") {
		return 0, false
	}
	protocol := strings.ToLower(stringValue(rule, "protocol"))
	if protocol != "tcp" && protocol != "*" {
		return 0, false
	}
	sources := append([]string{stringValue(rule, "source_address_prefix")}, stringList(rule, "source_address_prefixes")...)
	if !slices.ContainsFunc(sources, internetSource) {
		return 0, false
	}
	ranges := append([]string{stringValue(rule, "destination_port_range")}, stringList(rule, "destination_port_ranges")...)
	for _, port := range adminPorts {
		for _, portRange := range ranges {
			if portInRange(port, portRange) {
				return port, true
			}
		}
	}
	return 0, false
}

func portInRange(port int, portRange string) bool {
	portRange = strings.TrimSpace(portRange)
	if portRange == "*" {
		return true
	}
	low, high, isRange := strings.Cut(portRange, "-")
	from, err := strconv.Atoi(strings.TrimSpace(low))
	if err != nil {
		return false
	}
	to := from
	if isRange {
		if to, err = strconv.Atoi(strings.TrimSpace(high)); err != nil {
			return false
		}
	}
	return port >= from && port <= to
}

// policyDocument is an IAM or bucket policy. Fields that can be a string or a list are decoded as any.
type policyDocument struct {
	Statement json.RawMessage `json:"Statement"`
}

type policyStatement struct {
	Effect    string `json:"Effect"`
	Principal any    `json:"Principal"`
	Action    any    `json:"Action"`
	Resource  any    `json:"Resource"`
	Condition any    `json:"Condition"`
}

func policyStatements(policy string) []policyStatement {
	if policy == "" {
		return nil
	}
	var doc policyDocument
	if json.Unmarshal([]byte(policy), &doc) != nil || len(doc.Statement) == 0 {
		return nil
	}
	var list []policyStatement
	if json.Unmarshal(doc.Statement, &list) == nil {
		return list
	}
	var single policyStatement
	if json.Unmarshal(doc.Statement, &single) == nil {
		return []policyStatement{single}
	}
	return nil
}

// containsStar reports whether a policy element is "*" or a list containing "*".
func containsStar(value any) bool {
	switch v := value.(type) {
	case string:
		return v == "*"
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == "*" {
				return true
			}
		}
	}
	return false
}

func wildcardPolicy(policy string) bool {
	for _, statement := range policyStatements(policy) {
		if statement.Effect == "Allow" && containsStar(statement.Action) && containsStar(statement.Resource) {
			return true
		}
	}
	return false
}

func publicPolicy(policy string) bool {
	for _, statement := range policyStatements(policy) {
		if statement.Effect != "Allow" || statement.Condition != nil {
			continue
		}
		principal := statement.Principal
		if m, ok := principal.(map[string]any); ok {
			principal = m["AWS"]
		}
		if containsStar(principal) {
			return true
		}
	}
	return false
}
