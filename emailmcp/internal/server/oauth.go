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
	oauthRefreshTTL = 30 * 24 * time.Hour
	oauthClientTTL  = 90 * 24 * time.Hour
)

// OAuthServer is a thin OAuth 2.1 authorization server that fronts Google
// sign-in for MCP clients. It issues the Google ID token as the access_token
// so existing EmailMCP / Config API verification continues to work.
type OAuthServer struct {
	baseURL      string
	googleConfig *oauth2.Config
	logger       *slog.Logger
	httpClient   *http.Client

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

// NewOAuthServer builds the Google-backed OAuth authorization server.
func NewOAuthServer(baseURL, clientID, clientSecret string, logger *slog.Logger) (*OAuthServer, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("PUBLIC_BASE_URL is required for OAuth")
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET are required for OAuth")
	}
	if logger == nil {
		logger = slog.Default()
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
		logger:     logger,
		httpClient: http.DefaultClient,
		pending:    make(map[string]*pendingAuth),
		codes:      make(map[string]*issuedCode),
		refresh:    make(map[string]*refreshEntry),
		clients:    make(map[string]*oauthClient),
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
	meta := map[string]any{
		"issuer":                                o.baseURL,
		"authorization_endpoint":                o.baseURL + "/oauth/authorize",
		"token_endpoint":                        o.baseURL + "/oauth/token",
		"registration_endpoint":                 o.baseURL + "/oauth/register",
		"jwks_uri":                              "https://www.googleapis.com/oauth2/v3/certs",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
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
		if !isAllowedRedirectURI(u) {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri not allowed: "+u)
			return
		}
	}

	clientID, err := randomToken(16)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	authMethod := req.TokenEndpointAuthMethod
	if authMethod == "" {
		authMethod = "none"
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
		"token_endpoint_auth_method": authMethod,
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
	if !isAllowedRedirectURI(redirectURI) {
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

	// Optional client authentication (public clients use none).
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		clientID = basicID
		_ = basicSecret
	}
	if formSecret := r.FormValue("client_secret"); formSecret != "" && clientID == "" {
		clientID = r.FormValue("client_id")
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

	expiresIn := int(time.Until(issued.Expiry).Seconds())
	if expiresIn < 60 {
		expiresIn = 3600
	}

	resp := map[string]any{
		"access_token": issued.IDToken,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
		"scope":        "openid email profile",
		"id_token":     issued.IDToken,
	}

	if issued.GoogleRefresh != "" {
		ourRefresh, err := randomToken(32)
		if err == nil {
			o.mu.Lock()
			o.refresh[ourRefresh] = &refreshEntry{
				ClientID:      issued.ClientID,
				GoogleRefresh: issued.GoogleRefresh,
				ExpiresAt:     time.Now().Add(oauthRefreshTTL),
			}
			o.mu.Unlock()
			resp["refresh_token"] = ourRefresh
		}
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

	expiresIn := int(time.Until(newTok.Expiry).Seconds())
	if expiresIn < 60 {
		expiresIn = 3600
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  idToken,
		"token_type":    "Bearer",
		"expires_in":    expiresIn,
		"scope":         "openid email profile",
		"id_token":      idToken,
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

func isAllowedRedirectURI(u string) bool {
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
		return parsed.Hostname() != ""
	}
	// Custom URI schemes (desktop / native clients).
	if scheme != "javascript" && scheme != "data" && scheme != "vbscript" && scheme != "file" {
		return parsed.Scheme != "" && !strings.Contains(parsed.Scheme, ".")
	}
	return false
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
