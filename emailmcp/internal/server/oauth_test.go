package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testOAuthServer(t *testing.T) *OAuthServer {
	t.Helper()
	tokens, err := NewTokenIssuer([]byte("test-signing-secret"), "https://emailmcp.ecg.co", accessTokenTTL)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	o, err := NewOAuthServer("https://emailmcp.ecg.co", "client-id.apps.googleusercontent.com", "client-secret", tokens, nil)
	if err != nil {
		t.Fatalf("NewOAuthServer: %v", err)
	}
	return o
}

func TestProtectedResourceMetadata(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "https://emailmcp.ecg.co/.well-known/oauth-protected-resource", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["resource"] != "https://emailmcp.ecg.co" {
		t.Errorf("resource = %v", meta["resource"])
	}
	servers, ok := meta["authorization_servers"].([]any)
	if !ok || len(servers) != 1 || servers[0] != "https://emailmcp.ecg.co" {
		t.Errorf("authorization_servers = %v", meta["authorization_servers"])
	}
}

func TestAuthServerMetadata(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "https://emailmcp.ecg.co/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var meta map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["authorization_endpoint"] != "https://emailmcp.ecg.co/oauth/authorize" {
		t.Errorf("authorization_endpoint = %v", meta["authorization_endpoint"])
	}
	if meta["token_endpoint"] != "https://emailmcp.ecg.co/oauth/token" {
		t.Errorf("token_endpoint = %v", meta["token_endpoint"])
	}
	if meta["registration_endpoint"] != "https://emailmcp.ecg.co/oauth/register" {
		t.Errorf("registration_endpoint = %v", meta["registration_endpoint"])
	}
}

func TestDynamicClientRegistration(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	body := `{"client_name":"Test Client","redirect_uris":["http://127.0.0.1:54321/callback"]}`
	req := httptest.NewRequest(http.MethodPost, "https://emailmcp.ecg.co/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	clientID, _ := resp["client_id"].(string)
	if clientID == "" {
		t.Fatal("expected client_id")
	}
	o.mu.Lock()
	_, ok := o.clients[clientID]
	o.mu.Unlock()
	if !ok {
		t.Fatal("client not stored")
	}
}

func TestAuthorizeRedirectsToGoogle(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	// Register client first.
	regBody := `{"redirect_uris":["http://127.0.0.1:9999/cb"],"client_name":"t"}`
	regReq := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(regBody))
	regReq.Header.Set("Content-Type", "application/json")
	regRec := httptest.NewRecorder()
	mux.ServeHTTP(regRec, regReq)
	var reg map[string]any
	_ = json.Unmarshal(regRec.Body.Bytes(), &reg)
	clientID := reg["client_id"].(string)

	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {"http://127.0.0.1:9999/cb"},
		"state":                 {"client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {"openid email profile"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.Host, "accounts.google.com") {
		t.Fatalf("expected Google host, got %s", u.Host)
	}
	if u.Query().Get("client_id") != "client-id.apps.googleusercontent.com" {
		t.Errorf("client_id = %s", u.Query().Get("client_id"))
	}
	if u.Query().Get("redirect_uri") != "https://emailmcp.ecg.co/oauth/callback" {
		t.Errorf("redirect_uri = %s", u.Query().Get("redirect_uri"))
	}
	if u.Query().Get("state") == "" {
		t.Fatal("expected google state")
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.pending) != 1 {
		t.Fatalf("pending count = %d", len(o.pending))
	}
}

func TestAuthorizeLoopbackWithoutRegistration(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	verifier := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUV"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"unregistered-client"},
		"redirect_uri":          {"http://localhost:8765/callback"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAuthorizeRejectsNonLoopbackUnregistered(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	sum := sha256.Sum256([]byte("verifier-value-with-enough-entropy-here"))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"unknown"},
		"redirect_uri":          {"https://evil.example/cb"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "/oauth/authorize?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !verifyPKCE(verifier, challenge, "S256") {
		t.Fatal("expected PKCE match")
	}
	if verifyPKCE("wrong", challenge, "S256") {
		t.Fatal("expected PKCE mismatch")
	}
}

func TestTokenRejectsMissingFields(t *testing.T) {
	o := testOAuthServer(t)
	mux := http.NewServeMux()
	o.Mount(mux)

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader("grant_type=authorization_code"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "invalid_request") {
		t.Fatalf("body = %s", body)
	}
}

func TestIsAllowedRedirectURI(t *testing.T) {
	cases := []struct {
		uri  string
		want bool
	}{
		{"http://127.0.0.1:1234/cb", true},
		{"http://localhost:8080/", true},
		{"https://app.example.com/oauth/cb", true},
		{"cursor://oauth/callback", true},
		{"javascript:alert(1)", false},
		{"data:text/html,hi", false},
		{"not-a-uri", false},
		{"http://evil.com/cb", false},
	}
	for _, tc := range cases {
		if got := isAllowedRedirectURI(tc.uri); got != tc.want {
			t.Errorf("isAllowedRedirectURI(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}
