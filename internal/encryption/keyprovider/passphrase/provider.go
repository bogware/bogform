// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package passphrase

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/bogware/bogform/internal/encryption/keyprovider"
	"github.com/bogware/bogform/internal/encryption/keyprovider/pbkdf2"
)

// MinimumLength is the shortest passphrase that pbkdf2 accepts.
const MinimumLength = pbkdf2.MinimumPassphraseLength

type provider struct {
	config Config
}

// cache keeps passphrases read from the keychain or the cloud during this run, because a command builds
// the key providers more than once, and those lookups are slow. Only successful lookups are kept, and
// only in memory. Environment variables are cheap to read, so they're never cached.
var cache sync.Map

func (p *provider) Provide(_ keyprovider.KeyMeta) (keyprovider.Output, keyprovider.KeyMeta, error) {
	value, err := p.resolve()
	if err != nil {
		return keyprovider.Output{}, nil, err
	}
	key := []byte(value)
	return keyprovider.Output{EncryptionKey: key, DecryptionKey: key}, nil, nil
}

type source struct {
	kind, name string
	lookup     func() (string, error)
	cached     bool
}

func (p *provider) sources() []source {
	c := p.config
	var result []source
	if c.Env != "" {
		result = append(result, source{"environment variable", c.Env, func() (string, error) { return lookupEnv(c.Env) }, false})
	}
	if c.Keychain != "" {
		result = append(result, source{"keychain entry", c.Keychain, func() (string, error) { return lookupKeychain(c.Keychain) }, true})
	}
	if c.AWSSSM != "" {
		result = append(result, source{"SSM parameter", c.AWSSSM, func() (string, error) { return lookupSSM(c.AWSSSM, c.AWSRegion) }, true})
	}
	if c.AzureKeyVault != "" {
		result = append(result, source{"Key Vault secret", c.AzureKeyVault, func() (string, error) { return lookupKeyVault(c.AzureKeyVault) }, true})
	}
	return result
}

// resolve tries each source in order and returns the first passphrase found. The error lists every
// source and why it didn't work. It never includes the passphrase.
func (p *provider) resolve() (string, error) {
	var tried []string
	for _, s := range p.sources() {
		cacheKey := s.kind + "\x00" + s.name + "\x00" + p.config.AWSRegion
		if s.cached {
			if cached, ok := cache.Load(cacheKey); ok {
				return cached.(string), nil
			}
		}
		value, err := s.lookup()
		if err == nil && value == "" {
			err = errNotFound
		}
		if err != nil {
			reason := err.Error()
			if errors.Is(err, errNotFound) {
				reason = "not set"
			}
			tried = append(tried, fmt.Sprintf("%s %q: %s", s.kind, s.name, reason))
			continue
		}
		if len(value) < MinimumLength {
			return "", &keyprovider.ErrKeyProviderFailure{
				Message: fmt.Sprintf("the passphrase from %s %q is too short: it must have at least %d characters", s.kind, s.name, MinimumLength),
			}
		}
		if s.cached {
			cache.Store(cacheKey, value)
		}
		return value, nil
	}
	return "", &keyprovider.ErrKeyProviderFailure{
		Message: "couldn't find the state passphrase. Tried:\n  - " + strings.Join(tried, "\n  - "),
	}
}
