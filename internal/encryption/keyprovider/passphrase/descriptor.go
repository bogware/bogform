// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package passphrase contains a key provider that reads a passphrase from the environment, the OS
// keychain, AWS SSM Parameter Store or Azure Key Vault, and passes it to pbkdf2 through "chain". The
// same configuration can work in a pipeline, which sets an environment variable, and on a workstation,
// which reads the keychain or the cloud.
package passphrase

import (
	"github.com/bogware/bogform/internal/encryption/keyprovider"
)

// New creates the descriptor for the passphrase key provider.
func New() Descriptor {
	return &descriptor{}
}

// Descriptor describes the passphrase key provider.
type Descriptor interface {
	keyprovider.Descriptor
}

type descriptor struct{}

func (d descriptor) ID() keyprovider.ID {
	return "passphrase"
}

func (d descriptor) ConfigStruct() keyprovider.Config {
	return &Config{}
}
