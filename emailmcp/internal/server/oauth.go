package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	oauthCodeTTL    = 5 * time.Minute
	oauthPendingTTL = 15 * time.Minute
	oauthClientTTL  = 90 * 24 * time.Hour
)

// OAuthServer is a thin OAuth 2.1 authorization server that fronts Google
// sign-in for MCP clients. After Google verifies the user it issues the
// server's own short-lived JWT as the access_token (paired with a longer-lived
// refresh token). The Google ID token is embedded in the JWT so downstream
// EmailMCP / Config API verification continues to work.
type OAuthServer struct {
	baseURL      string
	googleConfig *oauth2.Config
	tokens       *TokenIssuer
	logger       *slog.Logger
	httpClient   *http.Client
	// redirectAllowlist restricts HTTPS redirect_uris when non-empty.
	// Empty means allowlist enforcement is off (any HTTPS host is accepted).
	redirectAllowlist []string

	mu        sync.Mutex
	pending   map[string]*pendingAuth  // google state -> pending
	codes     map[string]*issuedCode   // auth code -> issued
	refresh   map[string]*refreshEntry // our refresh token -> entry
	clients   map[string]*oauthClient  // client_id -> registration
	cleanupAt time.Time
}

type pendingAuth struct {
	ClientID            string
	RedirectURI         string
	ClientState         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	Resource            string
	ExpiresAt           time.Time
}

type issuedCode struct {
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	IDToken             string
	GoogleRefresh       string
	Expiry              time.Time
	ExpiresAt           time.Time
}

type refreshEntry struct {
	ClientID      string
	Subject       string
	Email         string
	GoogleRefresh string
	ExpiresAt     time.Time
}

type oauthClient struct {
	ClientID     string
	ClientSecret string
	RedirectURIs []string
	ClientName   string
	ExpiresAt    time.Time
}

// NewOAuthServer builds the Google-backed OAuth authorization server. tokens is
// used to mint the short-lived session JWTs returned as access_tokens.
// redirectAllowlist restricts HTTPS redirect_uris when non-empty; empty means
// the allowlist is not enforced.
func NewOAuthServer(baseURL, clientID, clientSecret string, tokens *TokenIssuer, logger *slog.Logger, redirectAllowlist []string) (*OAuthServer, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL is required for OAuth")
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required for OAuth")
	}
	if tokens == nil {
		return nil, fmt.Errorf("token issuer is required for OAuth")
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Defensive copy so callers cannot mutate the server's allowlist later.
	var allowlist []string
	if len(redirectAllowlist) > 0 {
		allowlist = append(allowlist, redirectAllowlist...)
	}

	return &OAuthServer{
		baseURL: baseURL,
		googleConfig: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  baseURL + "/oauth/callback",
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		},
		tokens:            tokens,
		logger:            logger,
		httpClient:        http.DefaultClient,
		redirectAllowlist: allowlist,
		pending:           make(map[string]*pendingAuth),
		codes:             make(map[string]*issuedCode),
		refresh:           make(map[string]*refreshEntry),
		clients:           make(map[string]*oauthClient),
	}, nil
}

// ResourceMetadataURL is the RFC 9728 protected resource metadata path.
func (o *OAuthServer) ResourceMetadataURL() string {
	return o.baseURL + "/.well-known/oauth-protected-resource"
}

// Mount registers unauthenticated OAuth discovery and flow endpoints on mux.
func (o *OAuthServer) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", o.handleProtectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", o.handleAuthServerMetadata)
	// Some clients look under the resource path as well.
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/", o.handleProtectedResourceMetadata)

	mux.HandleFunc("POST /oauth/register", o.handleRegister)
	mux.HandleFunc("GET /oauth/authorize", o.handleAuthorize)
	mux.HandleFunc("GET /oauth/callback", o.handleCallback)
	mux.HandleFunc("POST /oauth/token", o.handleToken)
}

func (o *OAuthServer) handleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	meta := map[string]any{
		"resource":                 o.baseURL,
		"authorization_servers":    []string{o.baseURL},
		"scopes_supported":         []string{"openid", "email", "profile"},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "EmailMCP",
	}
	writeJSON(w, http.StatusOK, meta)
}

func (o *OAuthServer) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	// Access tokens are local HS256 session JWTs (no public JWKS). Omit jwks_uri
	// rather than advertising Google's certs, which would mislead clients/scanners.
	// DCR clients are public (PKCE); only token_endpoint_auth_method "none" is supported.
	meta := map[string]any{
		"issuer":                                o.baseURL,
		"authorization_endpoint":                o.baseURL + "/oauth/authorize",
		"token_endpoint":                        o.baseURL + "/oauth/token",
		"registration_endpoint":                 o.baseURL + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"openid", "email", "profile"},
		"service_documentation":                 "https://github.com/jpuckett/EmailMCP",
	}
	writeJSON(w, http.StatusOK, meta)
}

// Dynamic Client Registration (RFC 7591) for public MCP clients.
func (o *OAuthServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
		Scope                   string   `json:"scope"`
		ClientURI               string   `json:"client_uri"`
		ApplicationType         string   `json:"application_type"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !o.isAllowedRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri not allowed: "+u)
			return
		}
	}

	clientID, err := randomToken(16)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Public clients only (MCP + PKCE). We do not issue or store client secrets,
	// so reject registrations that request confidential client auth methods.
	authMethod := req.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = "none"
	}
	if !strings.EqualFold(authMethod, "none") {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only token_endpoint_auth_method=none is supported (public clients with PKCE)")
		return
	}

	o.mu.Lock()
	o.clients[clientID] = &oauthClient{
		ClientID:     clientID,
		RedirectURIs: append([]string(nil), req.RedirectURIs...),
		ClientName:   req.ClientName,
		ExpiresAt:    time.Now().Add(oauthClientTTL),
	}
	o.mu.Unlock()

	o.logger.Info("oauth client registered", "client_id", clientID, "name", req.ClientName)

	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                req.ClientName,
		"redirect_uris":              req.RedirectURIs,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"scope":                      "openid email profile",
	})
}

func (o *OAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	responseType := q.Get("response_type")
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	scope := q.Get("scope")
	resource := q.Get("resource")

	if responseType != "code" {
		http.Error(w, "unsupported response_type", http.StatusBadRequest)
		return
	}
	if clientID == "" || redirectURI == "" {
		http.Error(w, "client_id and redirect_uri are required", http.StatusBadRequest)
		return
	}
	if codeChallenge == "" || !strings.EqualFold(codeChallengeMethod, "S256") {
		http.Error(w, "PKCE S256 code_challenge is required", http.StatusBadRequest)
		return
	}
	if !o.isAllowedRedirectURI(redirectURI) {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}

	o.mu.Lock()
	client, ok := o.clients[clientID]
	if ok {
		if time.Now().After(client.ExpiresAt) {
			delete(o.clients, clientID)
			ok = false
		}
	}
	o.mu.Unlock()

	// Allow unknown clients only when redirect URI is a loopback (common for
	// MCP clients that skip DCR or use Client ID Metadata Documents).
	if ok {
		if !containsURI(client.RedirectURIs, redirectURI) {
			http.Error(w, "redirect_uri not registered for client", http.StatusBadRequest)
			return
		}
	} else if !isLoopbackRedirectURI(redirectURI) {
		http.Error(w, "unknown client_id; register via /oauth/register first", http.StatusBadRequest)
		return
	}

	googleState, err := randomToken(24)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	o.mu.Lock()
	o.cleanupLocked()
	o.pending[googleState] = &pendingAuth{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		ClientState:         state,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		Scope:               scope,
		Resource:            resource,
		ExpiresAt:           time.Now().Add(oauthPendingTTL),
	}
	o.mu.Unlock()

	authURL := o.googleConfig.AuthCodeURL(googleState,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (o *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errParam := q.Get("error"); errParam != "" {
		http.Error(w, "google oauth error: "+errParam+" "+q.Get("error_description"), http.StatusBadRequest)
		return
	}
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	o.mu.Lock()
	pending, ok := o.pending[state]
	if ok {
		delete(o.pending, state)
	}
	o.mu.Unlock()
	if !ok || time.Now().After(pending.ExpiresAt) {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	tok, err := o.googleConfig.Exchange(ctx, code)
	if err != nil {
		o.logger.Error("google token exchange failed", "error", err)
		http.Error(w, "failed to exchange code with Google", http.StatusBadGateway)
		return
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		http.Error(w, "Google did not return an id_token; ensure openid scope is granted", http.StatusBadGateway)
		return
	}

	// Cryptographically verify the Google ID token (signature, aud, iss, exp).
	if _, _, err := verifyGoogleIDToken(ctx, idToken, o.googleConfig.ClientID); err != nil {
		o.logger.Error("google id token verification failed after code exchange", "error", err)
		http.Error(w, "failed to verify Google identity token", http.StatusBadGateway)
		return
	}

	authCode, err := randomToken(24)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	o.mu.Lock()
	o.codes[authCode] = &issuedCode{
		ClientID:            pending.ClientID,
		RedirectURI:         pending.RedirectURI,
		CodeChallenge:       pending.CodeChallenge,
		CodeChallengeMethod: pending.CodeChallengeMethod,
		IDToken:             idToken,
		GoogleRefresh:       tok.RefreshToken,
		Expiry:              tok.Expiry,
		ExpiresAt:           time.Now().Add(oauthCodeTTL),
	}
	o.mu.Unlock()

	redir, err := url.Parse(pending.RedirectURI)
	if err != nil {
		http.Error(w, "invalid redirect_uri", http.StatusInternalServerError)
		return
	}
	rq := redir.Query()
	rq.Set("code", authCode)
	if pending.ClientState != "" {
		rq.Set("state", pending.ClientState)
	}
	redir.RawQuery = rq.Encode()
	http.Redirect(w, r, redir.String(), http.StatusFound)
}

func (o *OAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return
	}

	grantType := r.FormValue("grant_type")
	switch grantType {
	case "authorization_code":
		o.tokenAuthorizationCode(w, r)
	case "refresh_token":
		o.tokenRefresh(w, r)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "supported: authorization_code, refresh_token")
	}
}

func (o *OAuthServer) tokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	redirectURI := r.FormValue("redirect_uri")
	clientID := r.FormValue("client_id")
	codeVerifier := r.FormValue("code_verifier")

	if code == "" || redirectURI == "" || codeVerifier == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri, and code_verifier are required")
		return
	}

	// Public clients only. Accept client_id from Basic user or form; ignore any
	// client_secret (we never issue secrets for DCR clients).
	if basicID, _, ok := r.BasicAuth(); ok && basicID != "" {
		clientID = basicID
	}

	o.mu.Lock()
	issued, ok := o.codes[code]
	if ok {
		delete(o.codes, code) // one-time use
	}
	o.mu.Unlock()
	if !ok || time.Now().After(issued.ExpiresAt) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired authorization code")
		return
	}
	if issued.RedirectURI != redirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if clientID != "" && issued.ClientID != "" && clientID != issued.ClientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	if !verifyPKCE(codeVerifier, issued.CodeChallenge, issued.CodeChallengeMethod) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	// Re-verify the Google ID token before minting a session JWT.
	subject, email, err := verifyGoogleIDToken(r.Context(), issued.IDToken, o.googleConfig.ClientID)
	if err != nil {
		o.logger.Error("google id token verification failed at token endpoint", "error", err)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "identity token is no longer valid; re-authorize")
		return
	}
	// Issue our own short-lived session JWT (1h). The Google ID token is embedded
	// so downstream Config API calls can still present a genuine Google token.
	accessToken, expiry, err := o.tokens.Issue(subject, email, issued.IDToken)
	if err != nil {
		o.logger.Error("failed to issue session jwt", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "failed to issue access token")
		return
	}

	resp := map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(expiry).Seconds()),
		"scope":        "openid email profile",
	}

	// Pair the JWT with a refresh token (7 days) for longer-lived access.
	if ourRefresh, err := randomToken(32); err == nil {
		o.mu.Lock()
		o.refresh[ourRefresh] = &refreshEntry{
			ClientID:      issued.ClientID,
			Subject:       subject,
			Email:         email,
			GoogleRefresh: issued.GoogleRefresh,
			ExpiresAt:     time.Now().Add(refreshTokenTTL),
		}
		o.mu.Unlock()
		resp["refresh_token"] = ourRefresh
	}

	writeJSON(w, http.StatusOK, resp)
}

func (o *OAuthServer) tokenRefresh(w http.ResponseWriter, r *http.Request) {
	refreshToken := r.FormValue("refresh_token")
	if refreshToken == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}

	o.mu.Lock()
	entry, ok := o.refresh[refreshToken]
	if ok && time.Now().After(entry.ExpiresAt) {
		delete(o.refresh, refreshToken)
		ok = false
	}
	o.mu.Unlock()
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh_token")
		return
	}

	tok := &oauth2.Token{RefreshToken: entry.GoogleRefresh}
	src := o.googleConfig.TokenSource(r.Context(), tok)
	newTok, err := src.Token()
	if err != nil {
		o.logger.Error("google refresh failed", "error", err)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "failed to refresh token with Google")
		return
	}
	idToken, _ := newTok.Extra("id_token").(string)
	if idToken == "" {
		// Google often does not re-issue id_token on refresh; fall back is not available.
		oauthError(w, http.StatusBadRequest, "invalid_grant", "Google did not return id_token on refresh; re-authorize")
		return
	}

	// Rotate refresh token if Google issued a new one.
	if newTok.RefreshToken != "" && newTok.RefreshToken != entry.GoogleRefresh {
		o.mu.Lock()
		entry.GoogleRefresh = newTok.RefreshToken
		o.mu.Unlock()
	}

	// Mint a fresh session JWT after verifying the new Google ID token.
	subject, email, err := verifyGoogleIDToken(r.Context(), idToken, o.googleConfig.ClientID)
	if err != nil {
		o.logger.Error("google id token verification failed on refresh", "error", err)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "identity token is no longer valid; re-authorize")
		return
	}
	if subject == "" {
		subject = entry.Subject
	}
	if email == "" {
		email = entry.Email
	}
	accessToken, expiry, err := o.tokens.Issue(subject, email, idToken)
	if err != nil {
		o.logger.Error("failed to issue session jwt on refresh", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "failed to issue access token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessToken,
		"token_type":    "Bearer",
		"expires_in":    int(time.Until(expiry).Seconds()),
		"scope":         "openid email profile",
		"refresh_token": refreshToken,
	})
}

func verifyPKCE(verifier, challenge, method string) bool {
	if !strings.EqualFold(method, "S256") {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtleConstantTimeEqual(computed, challenge)
}

func subtleConstantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func (o *OAuthServer) cleanupLocked() {
	now := time.Now()
	if now.Before(o.cleanupAt) {
		return
	}
	o.cleanupAt = now.Add(time.Minute)
	for k, v := range o.pending {
		if now.After(v.ExpiresAt) {
			delete(o.pending, k)
		}
	}
	for k, v := range o.codes {
		if now.After(v.ExpiresAt) {
			delete(o.codes, k)
		}
	}
	for k, v := range o.refresh {
		if now.After(v.ExpiresAt) {
			delete(o.refresh, k)
		}
	}
	for k, v := range o.clients {
		if now.After(v.ExpiresAt) {
			delete(o.clients, k)
		}
	}
}

// isAllowedRedirectURI reports whether u may be used as an OAuth redirect_uri.
func (o *OAuthServer) isAllowedRedirectURI(u string) bool {
	return isAllowedRedirectURI(u, o.redirectAllowlist)
}

// isAllowedRedirectURI validates redirect URIs. Loopback HTTP and custom
// schemes (desktop MCP clients) are always accepted. HTTPS is accepted when
// the host is non-empty and either the allowlist is empty (not enforced) or
// the URI matches an allowlist entry.
func isAllowedRedirectURI(u string, allowlist []string) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	// MCP clients commonly use loopback HTTP or custom schemes (e.g. cursor://).
	if scheme == "http" {
		return isLoopbackHost(parsed.Hostname())
	}
	if scheme == "https" {
		if parsed.Hostname() == "" {
			return false
		}
		// Blank allowlist → enforcement off (any HTTPS host).
		if len(allowlist) == 0 {
			return true
		}
		return matchesRedirectAllowlist(parsed, allowlist)
	}
	// Custom URI schemes (desktop / native clients).
	if scheme != "javascript" && scheme != "data" && scheme != "vbscript" && scheme != "file" {
		return parsed.Scheme != "" && !strings.Contains(parsed.Scheme, ".")
	}
	return false
}

// matchesRedirectAllowlist checks a parsed HTTPS redirect against allowlist
// entries. Each entry may be:
//   - a hostname: example.com
//   - a wildcard host: *.example.com (matches subdomains only)
//   - an https origin or URI: https://app.example.com or https://app.example.com/cb
//     (origin-only allows any path; a non-root path requires prefix match)
func matchesRedirectAllowlist(redirect *url.URL, allowlist []string) bool {
	host := strings.ToLower(redirect.Hostname())
	if host == "" {
		return false
	}
	redirPath := redirect.EscapedPath()
	if redirPath == "" {
		redirPath = "/"
	}

	for _, entry := range allowlist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "://") {
			ep, err := url.Parse(entry)
			if err != nil || !strings.EqualFold(ep.Scheme, "https") {
				continue
			}
			entryHost := strings.ToLower(ep.Hostname())
			if entryHost == "" || entryHost != host {
				continue
			}
			// If the entry specifies a non-default port, require a match.
			if ep.Port() != "" && ep.Port() != redirect.Port() {
				continue
			}
			entryPath := ep.EscapedPath()
			if entryPath == "" || entryPath == "/" {
				return true
			}
			// Exact path or prefix (so /cb matches /cb and /cb/extra).
			entryPath = strings.TrimRight(entryPath, "/")
			if redirPath == entryPath || strings.HasPrefix(redirPath, entryPath+"/") {
				return true
			}
			continue
		}
		// Hostname or *.hostname entry.
		if hostMatchesAllowlistEntry(host, entry) {
			return true
		}
	}
	return false
}

// hostMatchesAllowlistEntry matches host against entry (exact or *.suffix).
func hostMatchesAllowlistEntry(host, entry string) bool {
	entry = strings.ToLower(strings.TrimSpace(entry))
	// Strip accidental scheme if someone wrote example.com/path without scheme.
	if i := strings.Index(entry, "/"); i >= 0 {
		entry = entry[:i]
	}
	if entry == "" {
		return false
	}
	if strings.HasPrefix(entry, "*.") {
		suffix := entry[1:] // ".example.com"
		return strings.HasSuffix(host, suffix) && len(host) > len(suffix)
	}
	return host == entry
}

func isLoopbackRedirectURI(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") && isLoopbackHost(parsed.Hostname())
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}

func containsURI(list []string, want string) bool {
	for _, u := range list {
		if u == want {
			return true
		}
	}
	return false
}

func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": desc,
	})
}
