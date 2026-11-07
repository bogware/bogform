// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package passphrase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/zalando/go-keyring"
)

// KeychainService is the service name of Bogform's entries in the OS keychain.
const KeychainService = "bogform"

// lookupTimeout limits each network lookup, so that an unreachable service doesn't hang a command.
const lookupTimeout = 20 * time.Second

// errNotFound means that a source is configured, but holds no passphrase. Other errors mean that the
// source couldn't be read.
var errNotFound = errors.New("not set")

// The lookups are variables so that tests can replace them.
var (
	lookupEnv = func(name string) (string, error) {
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			return "", errNotFound
		}
		return value, nil
	}

	lookupKeychain = func(name string) (string, error) {
		value, err := keyring.Get(KeychainService, name)
		if errors.Is(err, keyring.ErrNotFound) {
			return "", errNotFound
		}
		return value, err
	}

	lookupSSM = func(name, region string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		var opts []func(*awsconfig.LoadOptions) error
		if region != "" {
			opts = append(opts, awsconfig.WithRegion(region))
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			return "", err
		}
		out, err := ssm.NewFromConfig(cfg).GetParameter(ctx, &ssm.GetParameterInput{
			Name:           aws.String(name),
			WithDecryption: aws.Bool(true),
		})
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return "", errNotFound
		}
		if err != nil {
			return "", err
		}
		if out.Parameter == nil || out.Parameter.Value == nil {
			return "", errNotFound
		}
		return *out.Parameter.Value, nil
	}

	lookupKeyVault = func(secretURL string) (string, error) {
		vaultURL, name, version, err := parseKeyVaultSecret(secretURL)
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
		defer cancel()
		credential, err := azidentity.NewDefaultAzureCredential(nil)
		if err != nil {
			return "", err
		}
		client, err := azsecrets.NewClient(vaultURL, credential, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.GetSecret(ctx, name, version, nil)
		if err != nil {
			return "", err
		}
		if resp.Value == nil {
			return "", errNotFound
		}
		return *resp.Value, nil
	}
)

// SetKeychain stores a passphrase in the OS keychain under the given name.
func SetKeychain(name, value string) error {
	if name == "" {
		return errors.New("the keychain entry needs a name")
	}
	if len(value) < MinimumLength {
		return fmt.Errorf("the passphrase is too short: it must have at least %d characters", MinimumLength)
	}
	return keyring.Set(KeychainService, name, value)
}

// DeleteKeychain removes a passphrase from the OS keychain. It reports whether there was one.
func DeleteKeychain(name string) (bool, error) {
	err := keyring.Delete(KeychainService, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
