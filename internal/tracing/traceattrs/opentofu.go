// Copyright (c) The OpenTofu Authors
// SPDX-License-Identifier: MPL-2.0
// Copyright (c) 2023 HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package traceattrs

import (
	"go.opentelemetry.io/otel/attribute"
)

// This file contains some functions representing Bogform-specific semantic
// conventions, which we use alongside the general OpenTelemetry-specified
// semantic conventions.
//
// These functions tend to take strings that are expected to be the canonical
// string representation of some more specific type from elsewhere in Bogform,
// but we make the caller produce the string representation rather than doing it
// inline because this package needs to avoid importing any other packages
// from this codebase so that the rest of Bogform can use this package without
// creating import cycles.
//
// We only create functions in here for attribute names that we want to use
// consistently across many different callers. For one-off attribute names that
// are only used in a single kind of span, use the generic functions like
// [String], [StringSlice], etc, instead.

// BogformProviderAddress returns an attribute definition for indicating
// which provider is relevant to a particular trace span.
//
// The given address should be the result of calling [addrs.Provider.String].
func BogformProviderAddress(addr string) attribute.KeyValue {
	return attribute.String("opentofu.provider.address", addr)
}

// BogformProviderVersion returns an attribute definition for indicating
// which version of a provider is relevant to a particular trace span.
//
// The given address should be the result of calling
// [getproviders.Version.String]. This should typically be used alongside
// [BogformProviderAddress] to indicate which provider the version number is
// for.
func BogformProviderVersion(v string) attribute.KeyValue {
	return attribute.String("opentofu.provider.version", v)
}

// BogformTargetPlatform returns an attribute definition for indicating
// which target platform is relevant to a particular trace span.
//
// The given address should be the result of calling
// [getproviders.Platform.String].
func BogformTargetPlatform(platform string) attribute.KeyValue {
	return attribute.String("opentofu.target_platform", platform)
}

// BogformModuleCallName returns an attribute definition for indicating
// the name of a module call that's relevant to a particular trace span.
//
// The given address should be something that would be valid in the
// [addrs.ModuleCall.Name] field.
func BogformModuleCallName(name string) attribute.KeyValue {
	return attribute.String("opentofu.module.name", name)
}

// BogformModuleSource returns an attribute definition for indicating
// which module source address is relevant to a particular trace span.
//
// The given address should be the result of calling
// [addrs.ModuleSource.String], or any other syntax-compatible representation.
func BogformModuleSource(addr string) attribute.KeyValue {
	return attribute.String("opentofu.module.source", addr)
}

// BogformModuleVersion returns an attribute definition for indicating
// which version of a module is relevant to a particular trace span.
//
// The given address should be either the result of calling
// [getproviders.Version.String], or the String method from the "Version" type
// from HashiCorp's "go-version" library.
func BogformModuleVersion(v string) attribute.KeyValue {
	return attribute.String("opentofu.module.version", v)
}

// BogformOCIReferenceTag returns an attribute definition for indicating
// which OCI repository tag is relevant to a particular trace span.
func BogformOCIReferenceTag(name string) attribute.KeyValue {
	return attribute.String("opentofu.oci.reference.tag", name)
}

// BogformOCIReferenceDigest returns an attribute definition for indicating
// which OCI digest reference is relevant to a particular trace span.
func BogformOCIReferenceDigest(digest string) attribute.KeyValue {
	return attribute.String("opentofu.oci.reference.digest", digest)
}

// BogformOCIManifestMediaType returns an attribute definition for indicating
// which OCI manifest media type is relevant to a particular trace span.
func BogformOCIManifestMediaType(typ string) attribute.KeyValue {
	return attribute.String("opentofu.oci.manifest.media_type", typ)
}

// BogformOCIManifestArtifactType returns an attribute definition for indicating
// which OCI manifest artifact type is relevant to a particular trace span.
func BogformOCIManifestArtifactType(typ string) attribute.KeyValue {
	return attribute.String("opentofu.oci.manifest.artifact_type", typ)
}

// BogformOCIManifestSize returns an attribute definition for indicating
// the size in bytes of an OCI manifest that is relevant to a particular
// trace span.
func BogformOCIManifestSize(size int64) attribute.KeyValue {
	return attribute.Int64("opentofu.oci.manifest.size", size)
}

// BogformOCIBlobDigest returns an attribute definition for indicating
// which OCI blob digest is relevant to a particular trace span.
func BogformOCIBlobDigest(digest string) attribute.KeyValue {
	return attribute.String("opentofu.oci.blob.digest", digest)
}

// BogformOCIBlobMediaType returns an attribute definition for indicating
// which OCI blob media type is relevant to a particular trace span.
func BogformOCIBlobMediaType(typ string) attribute.KeyValue {
	return attribute.String("opentofu.oci.blob.media_type", typ)
}

// BogformOCIBlobArtifactType returns an attribute definition for indicating
// which OCI blob artifact type is relevant to a particular trace span.
func BogformOCIBlobArtifactType(typ string) attribute.KeyValue {
	return attribute.String("opentofu.oci.blob.artifact_type", typ)
}

// BogformOCIBlobSize returns an attribute definition for indicating
// the size in bytes of an OCI blob that is relevant to a particular
// trace span.
func BogformOCIBlobSize(size int64) attribute.KeyValue {
	return attribute.Int64("opentofu.oci.blob.size", size)
}

// BogformOCIRegistryDomain returns an attribute definition for indicating
// which OCI registry domain name is relevant to a particular trace span.
func BogformOCIRegistryDomain(domain string) attribute.KeyValue {
	return attribute.String("opentofu.oci.registry.domain", domain)
}

// BogformOCIRepositoryName returns an attribute definition for indicating
// which OCI repository is relevant to a particular trace span.
//
// The value of this should not include the registry domain name. Use a
// separate attribute built from [BogformOCIRegistryDomain] for that.
func BogformOCIRepositoryName(name string) attribute.KeyValue {
	return attribute.String("opentofu.oci.repository.name", name)
}
