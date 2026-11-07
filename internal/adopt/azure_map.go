// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package adopt

import (
	"fmt"
	"strings"
)

// azureMapping maps an Azure resource type to an azurerm resource type.
type azureMapping struct {
	// canonical is the type with the casing that azurerm's importers expect in IDs.
	canonical string
	tfType    string
	// byKind chooses a type from the resource's kind, for types like App Service sites.
	byKind func(kind string) string
	note   string
}

var azureMappings = []azureMapping{
	{canonical: "Microsoft.Network/dnsZones", tfType: "azurerm_dns_zone", note: "Records aren't discovered. Add import blocks for the ones you manage."},
	{canonical: "Microsoft.Network/privateDnsZones", tfType: "azurerm_private_dns_zone"},
	{canonical: "Microsoft.Network/virtualNetworks", tfType: "azurerm_virtual_network"},
	{canonical: "Microsoft.Network/networkSecurityGroups", tfType: "azurerm_network_security_group"},
	{canonical: "Microsoft.Network/publicIPAddresses", tfType: "azurerm_public_ip"},
	{canonical: "Microsoft.Network/networkInterfaces", tfType: "azurerm_network_interface"},
	{canonical: "Microsoft.Network/loadBalancers", tfType: "azurerm_lb"},
	{canonical: "Microsoft.Network/applicationGateways", tfType: "azurerm_application_gateway"},
	{canonical: "Microsoft.Network/natGateways", tfType: "azurerm_nat_gateway"},
	{canonical: "Microsoft.Network/routeTables", tfType: "azurerm_route_table"},
	{canonical: "Microsoft.Compute/virtualMachines", tfType: "azurerm_linux_virtual_machine", note: "If this virtual machine runs Windows, change the type to azurerm_windows_virtual_machine."},
	{canonical: "Microsoft.Compute/disks", tfType: "azurerm_managed_disk"},
	{canonical: "Microsoft.Compute/availabilitySets", tfType: "azurerm_availability_set"},
	{canonical: "Microsoft.Storage/storageAccounts", tfType: "azurerm_storage_account"},
	{canonical: "Microsoft.Web/serverFarms", tfType: "azurerm_service_plan"},
	{canonical: "Microsoft.Web/sites", byKind: func(kind string) string {
		k := strings.ToLower(kind)
		linux := strings.Contains(k, "linux")
		switch {
		case strings.Contains(k, "functionapp") && linux:
			return "azurerm_linux_function_app"
		case strings.Contains(k, "functionapp"):
			return "azurerm_windows_function_app"
		case linux:
			return "azurerm_linux_web_app"
		default:
			return "azurerm_windows_web_app"
		}
	}},
	{canonical: "Microsoft.Web/staticSites", tfType: "azurerm_static_web_app"},
	{canonical: "Microsoft.KeyVault/vaults", tfType: "azurerm_key_vault", note: "Only the vault is imported, not its secrets, keys or access policies."},
	{canonical: "Microsoft.Sql/servers", tfType: "azurerm_mssql_server"},
	{canonical: "Microsoft.Sql/servers/databases", tfType: "azurerm_mssql_database"},
	{canonical: "Microsoft.DBforPostgreSQL/flexibleServers", tfType: "azurerm_postgresql_flexible_server"},
	{canonical: "Microsoft.DBforMySQL/flexibleServers", tfType: "azurerm_mysql_flexible_server"},
	{canonical: "Microsoft.DocumentDB/databaseAccounts", tfType: "azurerm_cosmosdb_account"},
	{canonical: "Microsoft.Cache/Redis", tfType: "azurerm_redis_cache"},
	{canonical: "Microsoft.ContainerService/managedClusters", tfType: "azurerm_kubernetes_cluster"},
	{canonical: "Microsoft.ContainerRegistry/registries", tfType: "azurerm_container_registry"},
	{canonical: "Microsoft.App/containerApps", tfType: "azurerm_container_app"},
	{canonical: "Microsoft.App/managedEnvironments", tfType: "azurerm_container_app_environment"},
	{canonical: "Microsoft.ManagedIdentity/userAssignedIdentities", tfType: "azurerm_user_assigned_identity"},
	{canonical: "Microsoft.OperationalInsights/workspaces", tfType: "azurerm_log_analytics_workspace"},
	{canonical: "Microsoft.Insights/components", tfType: "azurerm_application_insights"},
	{canonical: "Microsoft.Insights/actionGroups", tfType: "azurerm_monitor_action_group"},
	{canonical: "Microsoft.ServiceBus/namespaces", tfType: "azurerm_servicebus_namespace"},
	{canonical: "Microsoft.EventHub/namespaces", tfType: "azurerm_eventhub_namespace"},
	{canonical: "Microsoft.Cdn/profiles", tfType: "azurerm_cdn_frontdoor_profile", note: "If this is a classic CDN profile rather than Front Door, change the type to azurerm_cdn_profile."},
	{canonical: "Microsoft.Communication/communicationServices", tfType: "azurerm_communication_service"},
	{canonical: "Microsoft.Communication/emailServices", tfType: "azurerm_email_communication_service"},
}

// MapAzure maps an Azure resource to the azurerm resource that manages it. The import ID is the
// resource ID, with its type segments in the casing azurerm expects.
func MapAzure(id, resourceType, name, kind string) (r Resource, ok bool, reason string) {
	for _, m := range azureMappings {
		if !strings.EqualFold(m.canonical, resourceType) {
			continue
		}
		tfType := m.tfType
		if m.byKind != nil {
			tfType = m.byKind(kind)
		}
		return Resource{
			Type:     tfType,
			ImportID: canonicalAzureID(id, m.canonical),
			Name:     lastSegment(name),
			Source:   resourceType,
			Note:     m.note,
		}, true, ""
	}
	return Resource{}, false, fmt.Sprintf("adopt doesn't support %s yet", resourceType)
}

// ResourceGroup returns the import block for a resource group.
func ResourceGroup(subscription, name string) Resource {
	return Resource{
		Type:     "azurerm_resource_group",
		ImportID: "/subscriptions/" + subscription + "/resourceGroups/" + name,
		Name:     name,
	}
}

// canonicalAzureID rewrites the provider namespace and type segments of id to match canonical,
// such as "Microsoft.Network/dnszones" to "Microsoft.Network/dnsZones". Azure returns some IDs in
// other casings, which some of azurerm's importers reject.
func canonicalAzureID(id, canonical string) string {
	segments := strings.Split(id, "/")
	typeParts := strings.Split(canonical, "/")
	provider := -1
	for i := len(segments) - 1; i >= 0; i-- {
		if strings.EqualFold(segments[i], "providers") {
			provider = i
			break
		}
	}
	if provider < 0 || provider+1 >= len(segments) {
		return id
	}
	// After "providers" come the namespace, then pairs of type and name.
	if strings.EqualFold(segments[provider+1], typeParts[0]) {
		segments[provider+1] = typeParts[0]
	}
	for t, pos := 1, provider+2; t < len(typeParts) && pos < len(segments); t, pos = t+1, pos+2 {
		if strings.EqualFold(segments[pos], typeParts[t]) {
			segments[pos] = typeParts[t]
		}
	}
	// Fix the resource group segment too.
	for i := 0; i+1 < len(segments) && i < provider; i++ {
		if strings.EqualFold(segments[i], "resourcegroups") {
			segments[i] = "resourceGroups"
		}
		if strings.EqualFold(segments[i], "subscriptions") {
			segments[i] = "subscriptions"
		}
	}
	return strings.Join(segments, "/")
}
