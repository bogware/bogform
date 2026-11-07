// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package passphrase

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/bogware/bogform/internal/encryption/keyprovider"
)

// Config lists where to look for the passphrase. Sources are tried in the order of the fields, and the
// first one that's configured and available is used.
type Config struct {
	// Env is the name of an environment variable.
	Env string `hcl:"env,optional"`
	// Keychain is the name of an entry in the OS keychain, stored by "bogform state passphrase set".
	Keychain string `hcl:"keychain,optional"`
	// AWSSSM is the name of an SSM parameter, usually a SecureString.
	AWSSSM string `hcl:"aws_ssm,optional"`
	// AWSRegion is the region of the SSM parameter. It defaults to the AWS SDK's usual settings.
	AWSRegion string `hcl:"aws_region,optional"`
	// AzureKeyVault is the URL of a Key Vault secret: https://<vault>.vault.azure.net/secrets/<name>,
	// optionally followed by a version.
	AzureKeyVault string `hcl:"azure_key_vault,optional"`
}

func (c *Config) Build() (keyprovider.KeyProvider, keyprovider.KeyMeta, error) {
	if c.Env == "" && c.Keychain == "" && c.AWSSSM == "" && c.AzureKeyVault == "" {
		return nil, nil, &keyprovider.ErrInvalidConfiguration{
			Message: "set at least one of env, keychain, aws_ssm or azure_key_vault",
		}
	}
	if c.AWSRegion != "" && c.AWSSSM == "" {
		return nil, nil, &keyprovider.ErrInvalidConfiguration{
			Message: "aws_region only applies with aws_ssm",
		}
	}
	if c.AzureKeyVault != "" {
		if _, _, _, err := parseKeyVaultSecret(c.AzureKeyVault); err != nil {
			return nil, nil, &keyprovider.ErrInvalidConfiguration{Message: err.Error()}
		}
	}
	// The provider stores no metadata. It always returns the same passphrase as both keys, and pbkdf2,
	// which it chains into, keeps the salt and parameters it needs to decrypt.
	return &provider{config: *c}, nil, nil
}

// parseKeyVaultSecret splits a Key Vault secret URL into the vault URL, the secret name and the
// optional version.
func parseKeyVaultSecret(raw string) (vaultURL, name, version string, err error) {
	invalid := fmt.Errorf("azure_key_vault must be a secret URL, such as https://my-vault.vault.azure.net/secrets/state-passphrase, not %q", raw)
	parsed, parseErr := url.Parse(raw)
	if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", invalid
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || parts[0] != "secrets" || parts[1] == "" {
		return "", "", "", invalid
	}
	if len(parts) == 3 {
		version = parts[2]
	}
	return "https://" + parsed.Host, parts[1], version, nil
}
