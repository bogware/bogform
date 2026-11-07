// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseJWK_ec(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, err := key.PublicKey.Bytes() // 0x04 || X || Y
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	jwk := func(crv, x, y string) json.RawMessage {
		raw, _ := json.Marshal(map[string]string{"kty": "EC", "kid": "k1", "crv": crv, "x": x, "y": y})
		return raw
	}

	kid, parsed, err := parseJWK(jwk("P-256", encode(point[1:33]), encode(point[33:])))
	if err != nil {
		t.Fatal(err)
	}
	if kid != "k1" {
		t.Errorf("wrong key ID %q", kid)
	}
	if public, ok := parsed.(*ecdsa.PublicKey); !ok || !public.Equal(&key.PublicKey) {
		t.Errorf("parsed a different key: %#v", parsed)
	}

	for name, raw := range map[string]json.RawMessage{
		"other curve":    jwk("P-384", encode(point[1:33]), encode(point[33:])),
		"short":          jwk("P-256", encode(point[1:20]), encode(point[33:])),
		"not on curve":   jwk("P-256", encode(make([]byte, 32)), encode(make([]byte, 32))),
		"invalid base64": jwk("P-256", "!!", encode(point[33:])),
	} {
		if _, _, err := parseJWK(raw); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
