// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package passphrase_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bogware/bogform/internal/configs"
	"github.com/bogware/bogform/internal/encryption"
	"github.com/bogware/bogform/internal/encryption/config"
	"github.com/bogware/bogform/internal/encryption/keyprovider/passphrase"
	"github.com/bogware/bogform/internal/encryption/keyprovider/pbkdf2"
	"github.com/bogware/bogform/internal/encryption/method/aesgcm"
	"github.com/bogware/bogform/internal/encryption/registry/lockingencryptionregistry"
)

const (
	chained = `
key_provider "passphrase" "state" {
  env = "BOG_TEST_STATE_PASSPHRASE"
}
key_provider "pbkdf2" "state" {
  chain = key_provider.passphrase.state
}
method "aes_gcm" "state" {
  keys = key_provider.pbkdf2.state
}
state {
  method   = method.aes_gcm.state
  enforced = true
}
`
	// direct is how repositories configured the passphrase before this provider existed.
	direct = `
key_provider "pbkdf2" "state" {
  passphrase = "correct horse battery staple"
}
method "aes_gcm" "state" {
  keys = key_provider.pbkdf2.state
}
state {
  method   = method.aes_gcm.state
  enforced = true
}
`
)

func stateEncryption(t *testing.T, source string) encryption.StateEncryption {
	t.Helper()
	reg := lockingencryptionregistry.New()
	if err := reg.RegisterKeyProvider(passphrase.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterKeyProvider(pbkdf2.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterMethod(aesgcm.New()); err != nil {
		t.Fatal(err)
	}
	cfg, diags := config.LoadConfigFromString("test.tf", source)
	if diags.HasErrors() {
		t.Fatal(diags.Error())
	}
	staticEval := configs.NewStaticEvaluator(nil, nil, configs.RootModuleCallForTesting())
	enc, diags := encryption.New(context.Background(), reg, cfg, staticEval)
	if diags.HasErrors() {
		t.Fatal(diags.Error())
	}
	return enc.State()
}

func TestChainRoundTrip(t *testing.T) {
	t.Setenv("BOG_TEST_STATE_PASSPHRASE", "correct horse battery staple")
	plain := []byte(`{"version": 4, "serial": 1, "lineage": "abc"}`)

	encrypted, err := stateEncryption(t, chained).EncryptState(plain)
	if err != nil {
		t.Fatal(err)
	}
	// Serial and lineage stay readable by design. The rest must be encrypted.
	if !strings.Contains(string(encrypted), `"encrypted_data"`) || strings.Contains(string(encrypted), `"version": 4`) {
		t.Fatal("the state wasn't encrypted")
	}
	decrypted, _, err := stateEncryption(t, chained).DecryptState(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != string(plain) {
		t.Fatal("round trip changed the state")
	}
}

// Moving the passphrase from a variable into this provider mustn't need the state re-encrypted.
func TestChainReadsStateFromDirectPassphrase(t *testing.T) {
	t.Setenv("BOG_TEST_STATE_PASSPHRASE", "correct horse battery staple")
	plain := []byte(`{"version": 4, "serial": 1, "lineage": "abc"}`)

	encrypted, err := stateEncryption(t, direct).EncryptState(plain)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, _, err := stateEncryption(t, chained).DecryptState(encrypted)
	if err != nil {
		t.Fatalf("couldn't read state written with a direct passphrase: %s", err)
	}
	if string(decrypted) != string(plain) {
		t.Fatal("wrong state")
	}

	// And the other way around, in case someone moves back.
	encrypted, err = stateEncryption(t, chained).EncryptState(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := stateEncryption(t, direct).DecryptState(encrypted); err != nil {
		t.Fatalf("couldn't read chained state with a direct passphrase: %s", err)
	}
}

func TestChainWrongPassphrase(t *testing.T) {
	t.Setenv("BOG_TEST_STATE_PASSPHRASE", "correct horse battery staple")
	encrypted, err := stateEncryption(t, chained).EncryptState([]byte(`{"version": 4, "serial": 1, "lineage": "abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOG_TEST_STATE_PASSPHRASE", "a different passphrase entirely")
	if _, _, err := stateEncryption(t, chained).DecryptState(encrypted); err == nil {
		t.Fatal("decrypted with the wrong passphrase")
	}
}
