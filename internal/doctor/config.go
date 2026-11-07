// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package doctor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// staticConfig is what doctor reads from the configuration's files directly. It doesn't evaluate
// anything, so it works before "init" and without credentials.
type staticConfig struct {
	Files []string

	RequiredVersion bool
	// Providers maps each required provider to whether it has a version constraint.
	Providers map[string]bool

	Backend     string            // The backend type, or "" if there isn't one.
	BackendArgs map[string]string // Literal string arguments of the backend.

	Encryption bool
	// StateEnforced is whether the encryption's state block sets enforced = true.
	StateEnforced bool
	KeyProviders  []keyProvider

	// Variables maps each variable with a literal string default to its default.
	VariableDefaults map[string]variableDefault
}

type keyProvider struct {
	Type, Name string
	// Literal string arguments.
	Args map[string]string
	// PassphraseVariable is the variable that a pbkdf2 passphrase comes from, like state_passphrase.
	PassphraseVariable string
	// PassphraseLiteral is set when the passphrase is written in the configuration.
	PassphraseLiteral bool
	// Chain names the key provider that a pbkdf2 passphrase is chained from, like passphrase.state.
	Chain string
}

type variableDefault struct {
	Value string
	File  string
	Line  int
}

// configFile reports whether a file is a native-syntax configuration file.
func configFile(name string) bool {
	for _, ext := range []string{".tf", ".tofu", ".bog"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

func readStaticConfig(dir string) (*staticConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	cfg := &staticConfig{
		Providers:        map[string]bool{},
		BackendArgs:      map[string]string{},
		VariableDefaults: map[string]variableDefault{},
	}
	for _, entry := range entries {
		if entry.IsDir() || !configFile(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		cfg.Files = append(cfg.Files, entry.Name())
		file, diags := hclsyntax.ParseConfig(src, entry.Name(), hcl.InitialPos)
		if diags.HasErrors() {
			// The configuration check reports syntax errors. Read what can be read.
			continue
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, block := range body.Blocks {
			switch block.Type {
			case "terraform":
				cfg.readTerraformBlock(block.Body)
			case "variable":
				if len(block.Labels) == 1 {
					if attr, ok := block.Body.Attributes["default"]; ok {
						if value, ok := literalString(attr.Expr); ok {
							cfg.VariableDefaults[block.Labels[0]] = variableDefault{Value: value, File: entry.Name(), Line: attr.SrcRange.Start.Line}
						}
					}
				}
			}
		}
	}
	sort.Strings(cfg.Files)
	return cfg, nil
}

func (cfg *staticConfig) readTerraformBlock(body *hclsyntax.Body) {
	if _, ok := body.Attributes["required_version"]; ok {
		cfg.RequiredVersion = true
	}
	for _, block := range body.Blocks {
		switch block.Type {
		case "required_providers":
			for name, attr := range block.Body.Attributes {
				cfg.Providers[name] = hasVersion(attr.Expr)
			}
		case "backend":
			if len(block.Labels) == 1 {
				cfg.Backend = block.Labels[0]
				for name, attr := range block.Body.Attributes {
					if value, ok := literalString(attr.Expr); ok {
						cfg.BackendArgs[name] = value
					}
				}
			}
		case "cloud":
			cfg.Backend = "cloud"
		case "encryption":
			cfg.Encryption = true
			cfg.readEncryptionBlock(block.Body)
		}
	}
}

func (cfg *staticConfig) readEncryptionBlock(body *hclsyntax.Body) {
	for _, block := range body.Blocks {
		switch block.Type {
		case "key_provider":
			if len(block.Labels) != 2 {
				continue
			}
			kp := keyProvider{Type: block.Labels[0], Name: block.Labels[1], Args: map[string]string{}}
			for name, attr := range block.Body.Attributes {
				if value, ok := literalString(attr.Expr); ok {
					kp.Args[name] = value
				}
				switch name {
				case "passphrase":
					if _, ok := literalString(attr.Expr); ok {
						kp.PassphraseLiteral = true
					} else if v := variableName(attr.Expr); v != "" {
						kp.PassphraseVariable = v
					}
				case "chain":
					if traversal, diags := hcl.AbsTraversalForExpr(attr.Expr); !diags.HasErrors() && len(traversal) == 3 {
						kp.Chain = traversalName(traversal[1]) + "." + traversalName(traversal[2])
					}
				}
			}
			cfg.KeyProviders = append(cfg.KeyProviders, kp)
		case "state":
			if attr, ok := block.Body.Attributes["enforced"]; ok {
				if value, diags := attr.Expr.Value(nil); !diags.HasErrors() && value.Type() == cty.Bool && value.True() {
					cfg.StateEnforced = true
				}
			}
		}
	}
}

// keyProvider finds a key provider by its type and name, like "passphrase.state".
func (cfg *staticConfig) keyProvider(address string) (keyProvider, bool) {
	for _, kp := range cfg.KeyProviders {
		if kp.Type+"."+kp.Name == address {
			return kp, true
		}
	}
	return keyProvider{}, false
}

func literalString(expr hclsyntax.Expression) (string, bool) {
	// A template with no interpolations is a literal.
	value, diags := expr.Value(nil)
	if diags.HasErrors() || !value.IsKnown() || value.IsNull() || value.Type() != cty.String {
		return "", false
	}
	return value.AsString(), true
}

func variableName(expr hclsyntax.Expression) string {
	traversal, diags := hcl.AbsTraversalForExpr(expr)
	if diags.HasErrors() || len(traversal) != 2 || traversal.RootName() != "var" {
		return ""
	}
	return traversalName(traversal[1])
}

func traversalName(step hcl.Traverser) string {
	switch s := step.(type) {
	case hcl.TraverseRoot:
		return s.Name
	case hcl.TraverseAttr:
		return s.Name
	}
	return ""
}

// hasVersion reports whether a required_providers entry has a version constraint. The old form, a
// bare string, is itself a constraint.
func hasVersion(expr hclsyntax.Expression) bool {
	switch e := expr.(type) {
	case *hclsyntax.ObjectConsExpr:
		for _, item := range e.Items {
			if key, diags := item.KeyExpr.Value(nil); !diags.HasErrors() && key.Type() == cty.String && key.AsString() == "version" {
				return true
			}
			// Unquoted keys are traversals.
			if traversal, diags := hcl.AbsTraversalForExpr(item.KeyExpr); !diags.HasErrors() && traversal.RootName() == "version" {
				return true
			}
		}
		return false
	default:
		_, ok := literalString(expr)
		return ok
	}
}
