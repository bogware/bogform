// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
)

const (
	sessionLifetime = 8 * time.Hour
	loginLifetime   = 10 * time.Minute
	sessionCookie   = "bogform_session"
	loginCookie     = "bogform_login"
)

// Identity is a person who has proved who they are.
type Identity struct {
	Email string
	Name  string
}

// Authenticator is a way of signing people in.
type Authenticator interface {
	// AuthCodeURL returns where to send the browser.
	AuthCodeURL(ctx context.Context, redirectURL, state, nonce, verifier string) (string, error)
	// Exchange completes the sign-in using the code that the provider returned.
	Exchange(ctx context.Context, redirectURL, code, nonce, verifier string) (Identity, error)
}

// cookieName uses the __Host- prefix on https, which stops subdomains from setting the cookie.
func (s *Server) cookieName(name string) string {
	if s.secureCookies() {
		return "__Host-" + name
	}
	return name
}

func (s *Server) secureCookies() bool {
	return strings.HasPrefix(s.cfg.PublicURL, "https://")
}

func (s *Server) setCookie(w http.ResponseWriter, name, value string, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(name),
		Value:    value,
		Path:     "/",
		MaxAge:   int(lifetime.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(),
		// Lax lets the provider's redirect back carry the login cookie, while blocking cross-site POSTs.
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(name), Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

// sign returns payload as JSON with an HMAC, under a purpose that stops one kind of signed value from
// being used as another.
func (s *Server) sign(purpose string, payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + base64.RawURLEncoding.EncodeToString(s.mac(purpose, body)), nil
}

func (s *Server) mac(purpose, body string) []byte {
	h := hmac.New(sha256.New, s.cfg.SessionKey)
	h.Write([]byte(purpose + "\x00" + body))
	return h.Sum(nil)
}

func (s *Server) verify(purpose, signed string, out any) bool {
	body, signature, found := strings.Cut(signed, ".")
	if !found {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(got, s.mac(purpose, body)) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

type sessionData struct {
	Email   string `json:"e"`
	Name    string `json:"n"`
	Expires int64  `json:"x"`
}

type loginData struct {
	State    string `json:"s"`
	Nonce    string `json:"o"`
	Verifier string `json:"v"`
	Expires  int64  `json:"x"`
}

func randomString() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// currentUser returns who is making a request and their role.
func (s *Server) currentUser(r *http.Request) (Identity, string) {
	if s.cfg.DevUser != "" {
		return Identity{Email: s.cfg.DevUser, Name: "Local developer"}, RoleOperator
	}
	cookie, err := r.Cookie(s.cookieName(sessionCookie))
	if err != nil {
		return Identity{}, RoleNone
	}
	var session sessionData
	if !s.verify(sessionCookie, cookie.Value, &session) || s.now().Unix() >= session.Expires {
		return Identity{}, RoleNone
	}
	return Identity{Email: session.Email, Name: session.Name}, s.cfg.RoleFor(session.Email)
}

func (s *Server) redirectURL() string {
	return s.cfg.PublicURL + "/api/v1/auth/callback"
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	state, err1 := randomString()
	nonce, err2 := randomString()
	if err := errors.Join(err1, err2); err != nil {
		s.fail(w, r, err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	target, err := s.auth.AuthCodeURL(r.Context(), s.redirectURL(), state, nonce, verifier)
	if err != nil {
		s.fail(w, r, fmt.Errorf("sign-in provider: %w", err))
		return
	}
	signed, err := s.sign(loginCookie, loginData{State: state, Nonce: nonce, Verifier: verifier, Expires: s.now().Add(loginLifetime).Unix()})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, loginCookie, signed, loginLifetime)
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(s.cookieName(loginCookie))
	var login loginData
	if err != nil || !s.verify(loginCookie, cookie.Value, &login) || s.now().Unix() >= login.Expires {
		s.authRedirect(w, r, "expired")
		return
	}
	s.clearCookie(w, loginCookie)
	if !hmac.Equal([]byte(r.URL.Query().Get("state")), []byte(login.State)) {
		s.authRedirect(w, r, "invalid")
		return
	}
	if r.URL.Query().Get("error") != "" {
		s.authRedirect(w, r, "denied")
		return
	}
	identity, err := s.auth.Exchange(r.Context(), s.redirectURL(), r.URL.Query().Get("code"), login.Nonce, login.Verifier)
	if err != nil {
		s.log("sign-in failed: %s", err)
		s.authRedirect(w, r, "failed")
		return
	}
	if s.cfg.RoleFor(identity.Email) == RoleNone {
		s.log("sign-in refused: %s isn't on the allowlist", identity.Email)
		s.authRedirect(w, r, "not-allowed")
		return
	}
	signed, err := s.sign(sessionCookie, sessionData{Email: strings.ToLower(identity.Email), Name: identity.Name, Expires: s.now().Add(sessionLifetime).Unix()})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setCookie(w, sessionCookie, signed, sessionLifetime)
	http.Redirect(w, r, "/", http.StatusFound)
}

// authRedirect returns to the app with a fixed code, so that nothing from the request is reflected.
func (s *Server) authRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/?auth_error="+code, http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	s.clearCookie(w, sessionCookie)
	w.WriteHeader(http.StatusNoContent)
}

// OIDC

type oidcAuthenticator struct {
	cfg    AuthConfig
	client *http.Client

	mu        sync.Mutex
	discovery *oidcDiscovery
	keys      map[string]any
	keysAt    time.Time
}

type oidcDiscovery struct {
	Issuer        string `json:"issuer"`
	AuthEndpoint  string `json:"authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

func (o *oidcAuthenticator) discover(ctx context.Context) (*oidcDiscovery, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.discovery != nil {
		return o.discovery, nil
	}
	var doc oidcDiscovery
	if err := getJSON(ctx, o.client, o.cfg.Issuer+"/.well-known/openid-configuration", nil, &doc); err != nil {
		return nil, err
	}
	// The specification requires this match, which stops a document from claiming another issuer's identity.
	if strings.TrimSuffix(doc.Issuer, "/") != strings.TrimSuffix(o.cfg.Issuer, "/") {
		return nil, fmt.Errorf("the provider identifies itself as %q, not the configured %q", doc.Issuer, o.cfg.Issuer)
	}
	o.discovery = &doc
	return &doc, nil
}

func (o *oidcAuthenticator) oauth(doc *oidcDiscovery, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     oauth2.Endpoint{AuthURL: doc.AuthEndpoint, TokenURL: doc.TokenEndpoint},
		Scopes:       []string{"openid", "email", "profile"},
	}
}

func (o *oidcAuthenticator) AuthCodeURL(ctx context.Context, redirectURL, state, nonce, verifier string) (string, error) {
	doc, err := o.discover(ctx)
	if err != nil {
		return "", err
	}
	return o.oauth(doc, redirectURL).AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce), oauth2.S256ChallengeOption(verifier)), nil
}

type oidcClaims struct {
	jwt.RegisteredClaims
	Nonce         string `json:"nonce"`
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Name          string `json:"name"`
}

func (o *oidcAuthenticator) Exchange(ctx context.Context, redirectURL, code, nonce, verifier string) (Identity, error) {
	doc, err := o.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, o.client)
	token, err := o.oauth(doc, redirectURL).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, errors.New("the provider didn't return an ID token")
	}
	claims := &oidcClaims{}
	_, err = jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return o.key(ctx, doc, kid)
	},
		jwt.WithValidMethods([]string{"RS256", "ES256"}),
		jwt.WithIssuer(doc.Issuer),
		jwt.WithAudience(o.cfg.ClientID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(time.Minute),
	)
	if err != nil {
		return Identity{}, fmt.Errorf("the ID token isn't valid: %w", err)
	}
	if !hmac.Equal([]byte(claims.Nonce), []byte(nonce)) {
		return Identity{}, errors.New("the ID token's nonce doesn't match")
	}
	if claims.Email == "" {
		return Identity{}, errors.New("the ID token has no email address. Check that the provider grants the email scope")
	}
	// Roles are based on email, so an unverified address could let someone claim to be somebody else.
	if !o.cfg.TrustEmail && (claims.EmailVerified == nil || !*claims.EmailVerified) {
		return Identity{}, errors.New("the provider doesn't say that this email address is verified. If your provider controls its users' addresses, set BOG_SERVER_OIDC_TRUST_EMAIL")
	}
	return Identity{Email: claims.Email, Name: claims.Name}, nil
}

// key returns a signing key from the provider. An unknown key ID triggers a refetch, to pick up key
// rotation, but no more than once a minute, so bad tokens can't flood the provider.
func (o *oidcAuthenticator) key(ctx context.Context, doc *oidcDiscovery, kid string) (any, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if key, ok := o.keys[kid]; ok {
		return key, nil
	}
	if time.Since(o.keysAt) < time.Minute && o.keys != nil {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := getJSON(ctx, o.client, doc.JWKSURI, nil, &set); err != nil {
		return nil, err
	}
	keys := map[string]any{}
	for _, raw := range set.Keys {
		if id, key, err := parseJWK(raw); err == nil {
			keys[id] = key
		}
	}
	o.keys = keys
	o.keysAt = time.Now()
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func parseJWK(raw json.RawMessage) (string, any, error) {
	var jwk struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
		Crv string `json:"crv"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return "", nil, err
	}
	if jwk.Use != "" && jwk.Use != "sig" {
		return "", nil, errors.New("not a signing key")
	}
	decode := func(value string) (*big.Int, error) {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			return nil, err
		}
		return new(big.Int).SetBytes(decoded), nil
	}
	switch jwk.Kty {
	case "RSA":
		n, err1 := decode(jwk.N)
		e, err2 := decode(jwk.E)
		if err := errors.Join(err1, err2); err != nil {
			return "", nil, err
		}
		return jwk.Kid, &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		if jwk.Crv != "P-256" {
			return "", nil, errors.New("unsupported curve")
		}
		x, err1 := base64.RawURLEncoding.DecodeString(jwk.X)
		y, err2 := base64.RawURLEncoding.DecodeString(jwk.Y)
		if err := errors.Join(err1, err2); err != nil {
			return "", nil, err
		}
		if len(x) != 32 || len(y) != 32 {
			return "", nil, errors.New("invalid P-256 key")
		}
		// An uncompressed point is 0x04 followed by the two coordinates.
		key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return "", nil, err
		}
		return jwk.Kid, key, nil
	}
	return "", nil, errors.New("unsupported key type")
}

// GitHub. GitHub's user sign-in is OAuth, not OIDC, so identity comes from its API.

type githubAuthenticator struct {
	cfg    AuthConfig
	client *http.Client
}

func (g *githubAuthenticator) oauth(redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.cfg.ClientID,
		ClientSecret: g.cfg.ClientSecret,
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:  g.cfg.GitHubURL + "/login/oauth/authorize",
			TokenURL: g.cfg.GitHubURL + "/login/oauth/access_token",
		},
		Scopes: []string{"read:user", "user:email"},
	}
}

func (g *githubAuthenticator) AuthCodeURL(_ context.Context, redirectURL, state, _, verifier string) (string, error) {
	return g.oauth(redirectURL).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

func (g *githubAuthenticator) Exchange(ctx context.Context, redirectURL, code, _, verifier string) (Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.client)
	token, err := g.oauth(redirectURL).Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, err
	}
	headers := map[string]string{"Authorization": "Bearer " + token.AccessToken, "Accept": "application/vnd.github+json"}
	var user struct {
		Name  string `json:"name"`
		Login string `json:"login"`
	}
	if err := getJSON(ctx, g.client, g.cfg.GitHubAPIURL+"/user", headers, &user); err != nil {
		return Identity{}, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, g.client, g.cfg.GitHubAPIURL+"/user/emails", headers, &emails); err != nil {
		return Identity{}, err
	}
	for _, email := range emails {
		if email.Primary && email.Verified {
			name := user.Name
			if name == "" {
				name = user.Login
			}
			return Identity{Email: email.Email, Name: name}, nil
		}
	}
	return Identity{}, errors.New("this GitHub account has no verified primary email address")
}

func getJSON(ctx context.Context, client *http.Client, target string, headers map[string]string, out any) error {
	if parsed, err := url.Parse(target); err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid URL %q", target)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", req.URL.Redacted(), res.StatusCode)
	}
	return json.Unmarshal(body, out)
}
