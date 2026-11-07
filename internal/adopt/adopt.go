// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package adopt finds existing cloud resources and writes import blocks for them, so that a
// configuration can take over infrastructure that was created some other way.
package adopt

import (
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

// Resource is an existing object, mapped to the Terraform resource that manages it.
type Resource struct {
	// Type is the resource type, such as aws_s3_bucket.
	Type string
	// ImportID is the ID that the provider's importer expects.
	ImportID string
	// Name is a readable name for the object, used to choose the resource's name in configuration.
	Name string
	// Source identifies the object in its cloud, such as an ARN or an Azure resource ID.
	Source string
	// Note is shown above the import block, for anything the user should check.
	Note string
}

// Skipped is an object that was found but has no import block.
type Skipped struct {
	Source string
	Reason string
}

// Result is what discovery found.
type Result struct {
	Resources []Resource
	Skipped   []Skipped
	// Warnings are problems that didn't stop discovery, such as a service that couldn't be listed.
	Warnings []string
}

// add records r, or skips it if its import ID looks personal.
func (res *Result) add(r Resource) {
	if LooksPersonal(r.ImportID) {
		res.Skipped = append(res.Skipped, Skipped{
			Source: r.Type,
			Reason: "its import ID looks like an email address, and import IDs appear in plan output. Import it from a workstation, with the ID in a sensitive variable or typed at the prompt",
		})
		return
	}
	for _, existing := range res.Resources {
		if existing.Type == r.Type && existing.ImportID == r.ImportID {
			return
		}
	}
	res.Resources = append(res.Resources, r)
}

func (res *Result) skip(source, reason string) {
	res.Skipped = append(res.Skipped, Skipped{Source: source, Reason: reason})
}

var emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// LooksPersonal reports whether an ID contains something personal, such as an email address.
func LooksPersonal(id string) bool {
	return emailRE.MatchString(id)
}

var nonIdentifier = regexp.MustCompile(`[^a-z0-9_]+`)

// ResourceName turns an object's name into a valid, readable resource name.
func ResourceName(name string) string {
	n := nonIdentifier.ReplaceAllString(strings.ToLower(name), "_")
	n = strings.Trim(n, "_")
	if n == "" {
		n = "this"
	}
	if n[0] >= '0' && n[0] <= '9' {
		n = "r_" + n
	}
	return n
}

// hclString quotes s as an HCL string, escaping template sequences so that IDs like "${x}" are
// taken literally.
func hclString(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
	s = strings.ReplaceAll(s, "${", "$${")
	s = strings.ReplaceAll(s, "%{", "%%{")
	return `"` + s + `"`
}

// WriteImports writes one import block for each resource, sorted by type and name, with unique
// resource names. It returns the addresses it used, in the same order as written.
func WriteImports(w io.Writer, header string, resources []Resource) ([]string, error) {
	sorted := append([]Resource(nil), resources...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].ImportID < sorted[j].ImportID
	})

	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(header), "\n") {
		if line == "" {
			b.WriteString("#\n")
		} else {
			b.WriteString("# " + line + "\n")
		}
	}
	used := map[string]int{}
	var addrs []string
	for _, r := range sorted {
		name := ResourceName(r.Name)
		key := r.Type + "." + name
		used[key]++
		if used[key] > 1 {
			name = fmt.Sprintf("%s_%d", name, used[key])
		}
		addr := r.Type + "." + name
		addrs = append(addrs, addr)

		b.WriteString("\n")
		if r.Source != "" && r.Source != r.ImportID {
			b.WriteString("# " + r.Source + "\n")
		}
		if r.Note != "" {
			b.WriteString("# " + r.Note + "\n")
		}
		b.WriteString("import {\n")
		b.WriteString("  to = " + addr + "\n")
		b.WriteString("  id = " + hclString(r.ImportID) + "\n")
		b.WriteString("}\n")
	}
	_, err := io.WriteString(w, b.String())
	return addrs, err
}
