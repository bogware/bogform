// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package passphrase

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bogware/bogform/internal/encryption/keyprovider"
	"github.com/bogware/bogform/internal/encryption/keyprovider/compliancetest"
)

const testPassphrase = "a perfectly good passphrase"

// stubSources replaces every lookup for the duration of a test, and clears the cache.
func stubSources(t *testing.T, env, keychain, ssm, keyVault func() (string, error)) {
	t.Helper()
	oldEnv, oldKeychain, oldSSM, oldKeyVault := lookupEnv, lookupKeychain, lookupSSM, lookupKeyVault
	resetCache()
	t.Cleanup(func() {
		lookupEnv, lookupKeychain, lookupSSM, lookupKeyVault = oldEnv, oldKeychain, oldSSM, oldKeyVault
		resetCache()
	})
	wrap := func(f func() (string, error)) func() (string, error) {
		if f == nil {
			return func() (string, error) {
				t.Fatal("a source was read that shouldn't have been")
				return "", nil
			}
		}
		return f
	}
	env, keychain, ssm, keyVault = wrap(env), wrap(keychain), wrap(ssm), wrap(keyVault)
	lookupEnv = func(string) (string, error) { return env() }
	lookupKeychain = func(string) (string, error) { return keychain() }
	lookupSSM = func(string, string) (string, error) { return ssm() }
	lookupKeyVault = func(string) (string, error) { return keyVault() }
}

func resetCache() {
	cache = sync.Map{}
}

func found(value string) func() (string, error) {
	return func() (string, error) { return value, nil }
}

func missing() (string, error) { return "", errNotFound }

func failing(message string) func() (string, error) {
	return func() (string, error) { return "", errors.New(message) }
}

func provide(t *testing.T, c Config) (keyprovider.Output, error) {
	t.Helper()
	p, meta, err := c.Build()
	if err != nil {
		t.Fatalf("Build failed: %s", err)
	}
	if meta != nil {
		t.Fatalf("expected no metadata, got %T", meta)
	}
	out, outMeta, err := p.Provide(nil)
	if outMeta != nil {
		t.Fatalf("expected no output metadata, got %T", outMeta)
	}
	return out, err
}

func TestSourceOrder(t *testing.T) {
	all := Config{Env: "PASS", Keychain: "repo", AWSSSM: "/repo/pass", AzureKeyVault: "https://v.vault.azure.net/secrets/pass"}

	t.Run("environment first", func(t *testing.T) {
		stubSources(t, found(testPassphrase), nil, nil, nil)
		out, err := provide(t, all)
		if err != nil {
			t.Fatal(err)
		}
		if string(out.EncryptionKey) != testPassphrase || string(out.DecryptionKey) != testPassphrase {
			t.Fatal("wrong keys")
		}
	})

	t.Run("falls through to the cloud", func(t *testing.T) {
		stubSources(t, missing, missing, failing("AccessDenied"), found(testPassphrase))
		out, err := provide(t, all)
		if err != nil {
			t.Fatal(err)
		}
		if string(out.EncryptionKey) != testPassphrase {
			t.Fatal("wrong key")
		}
	})

	t.Run("lists every source when none works", func(t *testing.T) {
		stubSources(t, missing, failing("no keychain"), failing("AccessDenied"), missing)
		_, err := provide(t, all)
		var failure *keyprovider.ErrKeyProviderFailure
		if !errors.As(err, &failure) {
			t.Fatalf("expected a key provider failure, got %v", err)
		}
		for _, want := range []string{`environment variable "PASS": not set`, `keychain entry "repo": no keychain`, `SSM parameter "/repo/pass": AccessDenied`, `Key Vault secret "https://v.vault.azure.net/secrets/pass": not set`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error doesn't mention %q:\n%s", want, err)
			}
		}
	})

	t.Run("only configured sources are read", func(t *testing.T) {
		stubSources(t, nil, nil, found(testPassphrase), nil)
		if _, err := provide(t, Config{AWSSSM: "/repo/pass"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("too short", func(t *testing.T) {
		stubSources(t, found("short"), nil, nil, nil)
		_, err := provide(t, Config{Env: "PASS"})
		if err == nil || !strings.Contains(err.Error(), "too short") {
			t.Fatalf("expected a length error, got %v", err)
		}
		if strings.Contains(err.Error(), "short\"") {
			t.Fatal("the error includes the passphrase")
		}
	})

	t.Run("an empty value counts as not set", func(t *testing.T) {
		stubSources(t, found(""), found(testPassphrase), nil, nil)
		if _, err := provide(t, Config{Env: "PASS", Keychain: "repo"}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestBuildValidation(t *testing.T) {
	cases := map[string]Config{
		"nothing":            {},
		"region without ssm": {Env: "PASS", AWSRegion: "us-east-1"},
		"not a vault url":    {AzureKeyVault: "my-vault/state"},
		"http vault url":     {AzureKeyVault: "http://v.vault.azure.net/secrets/pass"},
		"key not secret":     {AzureKeyVault: "https://v.vault.azure.net/keys/pass"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := c.Build(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParseKeyVaultSecret(t *testing.T) {
	vault, name, version, err := parseKeyVaultSecret("https://v.vault.azure.net/secrets/pass/0123abc")
	if err != nil || vault != "https://v.vault.azure.net" || name != "pass" || version != "0123abc" {
		t.Fatalf("got %q %q %q %v", vault, name, version, err)
	}
	_, _, version, err = parseKeyVaultSecret("https://v.vault.azure.net/secrets/pass")
	if err != nil || version != "" {
		t.Fatalf("got %q %v", version, err)
	}
}

func TestCompliance(t *testing.T) {
	stubSources(t, found(testPassphrase), found(testPassphrase), found(testPassphrase), found(testPassphrase))
	compliancetest.ComplianceTest(
		t,
		compliancetest.TestConfiguration[*descriptor, *Config, keyprovider.KeyMeta, *provider]{
			Descriptor: New().(*descriptor),
			HCLParseTestCases: map[string]compliancetest.HCLParseTestCase[*Config, *provider]{
				"empty": {
					HCL:        `key_provider "passphrase" "foo" {}`,
					ValidHCL:   true,
					ValidBuild: false,
				},
				"env": {
					HCL: `key_provider "passphrase" "foo" {
  env = "BOGFORM_STATE_PASSPHRASE"
}`,
					ValidHCL:   true,
					ValidBuild: true,
				},
				"all": {
					HCL: `key_provider "passphrase" "foo" {
  env             = "BOGFORM_STATE_PASSPHRASE"
  keychain        = "my-repo"
  aws_ssm         = "/my-repo/state-passphrase"
  aws_region      = "us-east-1"
  azure_key_vault = "https://my-vault.vault.azure.net/secrets/state-passphrase"
}`,
					ValidHCL:   true,
					ValidBuild: true,
					Validate: func(c *Config, _ *provider) error {
						if c.Keychain != "my-repo" || c.AWSRegion != "us-east-1" {
							return errors.New("attributes weren't parsed")
						}
						return nil
					},
				},
				"unknown attribute": {
					HCL: `key_provider "passphrase" "foo" {
  passphrase = "in the config"
}`,
					ValidHCL: false,
				},
			},
			JSONParseTestCases: map[string]compliancetest.JSONParseTestCase[*Config, *provider]{
				"env": {
					JSON:       `{"key_provider": {"passphrase": {"foo": {"env": "BOGFORM_STATE_PASSPHRASE"}}}}`,
					ValidJSON:  true,
					ValidBuild: true,
				},
				"empty": {
					JSON:       `{"key_provider": {"passphrase": {"foo": {}}}}`,
					ValidJSON:  true,
					ValidBuild: false,
				},
			},
			ConfigStructTestCases: map[string]compliancetest.ConfigStructTestCase[*Config, *provider]{
				"keychain": {Config: &Config{Keychain: "my-repo"}, ValidBuild: true},
				"empty":    {Config: &Config{}, ValidBuild: false},
			},
			ProvideTestCase: compliancetest.ProvideTestCase[*Config, keyprovider.KeyMeta]{
				ValidConfig: &Config{Env: "BOGFORM_STATE_PASSPHRASE"},
				ExpectedOutput: &keyprovider.Output{
					EncryptionKey: []byte(testPassphrase),
					DecryptionKey: []byte(testPassphrase),
				},
			},
		},
	)
}
