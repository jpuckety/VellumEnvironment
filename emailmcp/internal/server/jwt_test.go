package server

import (
	cryptorand "crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func newTestIssuer(t *testing.T, ttl time.Duration) *TokenIssuer {
	t.Helper()
	ti, err := NewTokenIssuer([]byte("unit-test-secret"), "https://emailmcp.ecg.co", ttl)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	return ti
}

func TestTokenIssuerRoundTrip(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)

	token, expiry, err := ti.Issue("google-sub-123", "user@example.com", "embedded-google-id-token")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	// Access token must live for ~1 hour.
	if d := time.Until(expiry); d < 55*time.Minute || d > accessTokenTTL+time.Minute {
		t.Fatalf("unexpected expiry window: %s", d)
	}

	claims, err := ti.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "google-sub-123" {
		t.Errorf("subject = %q", claims.Subject)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("email = %q", claims.Email)
	}
	if claims.GoogleIDToken != "embedded-google-id-token" {
		t.Errorf("gid_token = %q", claims.GoogleIDToken)
	}
}

func TestTokenIssuerAccessTTLIsOneHour(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)
	if ti.TTL() != time.Hour {
		t.Fatalf("access token TTL = %s, want 1h", ti.TTL())
	}
	if refreshTokenTTL != 7*24*time.Hour {
		t.Fatalf("refresh token TTL = %s, want 168h", refreshTokenTTL)
	}
}

func TestTokenIssuerRejectsExpired(t *testing.T) {
	ti := newTestIssuer(t, -time.Hour) // already expired

	// TTL<=0 falls back to the default; force an expired token by minting with a
	// dedicated issuer whose ttl is negative via a manual claim instead.
	past := time.Now().Add(-2 * time.Hour)
	claims := SessionClaims{
		Claims: jwt.Claims{
			Issuer:   "https://emailmcp.ecg.co",
			Subject:  "s",
			Audience: jwt.Audience{"https://emailmcp.ecg.co"},
			IssuedAt: jwt.NewNumericDate(past),
			Expiry:   jwt.NewNumericDate(past.Add(time.Minute)),
		},
	}
	token, err := jwt.Signed(ti.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := ti.Verify(token); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestTokenIssuerRejectsWrongKey(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)
	token, _, err := ti.Issue("s", "e", "g")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	other, err := NewTokenIssuer([]byte("a-different-secret"), "https://emailmcp.ecg.co", accessTokenTTL)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	if _, err := other.Verify(token); err == nil {
		t.Fatal("expected verification with a different key to fail")
	}
}

func TestTokenIssuerRejectsWrongIssuer(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)
	token, _, err := ti.Issue("s", "e", "g")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	other, err := NewTokenIssuer([]byte("unit-test-secret"), "https://evil.example", accessTokenTTL)
	if err != nil {
		t.Fatalf("NewTokenIssuer: %v", err)
	}
	if _, err := other.Verify(token); err == nil {
		t.Fatal("expected verification with a different issuer to fail")
	}
}

func TestVerifyGoogleIDTokenRejectsInvalid(t *testing.T) {
	// HS256 session JWTs and empty tokens must never pass Google verification.
	ti := newTestIssuer(t, accessTokenTTL)
	token, _, err := ti.Issue("sub-999", "person@example.com", "")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, _, err := verifyGoogleIDToken(t.Context(), token, "client-id.apps.googleusercontent.com"); err == nil {
		t.Fatal("expected HS256 token to fail Google id token verification")
	}
	if _, _, err := verifyGoogleIDToken(t.Context(), "", "client-id.apps.googleusercontent.com"); err == nil {
		t.Fatal("expected empty token to fail")
	}
	if _, _, err := verifyGoogleIDToken(t.Context(), "not-a-jwt", ""); err == nil {
		t.Fatal("expected missing audience to fail")
	}

	// Self-signed RS256 is not trusted by Google JWKS.
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	claims := jwt.Claims{
		Subject:  "google-sub",
		Audience: jwt.Audience{"client-id.apps.googleusercontent.com"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		Issuer:   "https://accounts.google.com",
	}
	extra := map[string]any{"email": "gmail@example.com", "email_verified": true}
	fakeGoogle, err := jwt.Signed(signer).Claims(claims).Claims(extra).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, _, err := verifyGoogleIDToken(t.Context(), fakeGoogle, "client-id.apps.googleusercontent.com"); err == nil {
		t.Fatal("expected self-signed RS256 token to fail Google JWKS verification")
	}
}

func TestTokenIssuerRejectsWrongAudience(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)
	// Mint a token whose aud does not match the issuer.
	now := time.Now()
	claims := SessionClaims{
		Claims: jwt.Claims{
			Issuer:    "https://emailmcp.ecg.co",
			Subject:   "s",
			Audience:  jwt.Audience{"https://evil.example"},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token, err := jwt.Signed(ti.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := ti.Verify(token); err == nil {
		t.Fatal("expected wrong audience to be rejected")
	}
}

func TestAuthenticatorMiddlewareAcceptsSessionJWT(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)
	a := NewAuthenticator(ti, "https://emailmcp.ecg.co")

	var gotUser *UserInfo
	var gotToken string
	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFromContext(r.Context())
		gotUser = u
		gotToken, _ = TokenFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	token, _, err := ti.Issue("sub-1", "u@example.com", "downstream-google-token")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "https://emailmcp.ecg.co/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotUser == nil || gotUser.Subject != "sub-1" || gotUser.Email != "u@example.com" {
		t.Fatalf("user = %+v", gotUser)
	}
	// The embedded Google token (not the session JWT) must be forwarded downstream.
	if gotToken != "downstream-google-token" {
		t.Fatalf("downstream token = %q, want embedded Google token", gotToken)
	}
}

func TestAuthenticatorMiddlewareRejectsBadToken(t *testing.T) {
	ti := newTestIssuer(t, accessTokenTTL)
	a := NewAuthenticator(ti, "https://emailmcp.ecg.co")
	handler := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "https://emailmcp.ecg.co/", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	// Client response must not leak verification error details.
	if body := rec.Body.String(); strings.Contains(body, "parse jwt") || strings.Contains(body, "compact") {
		t.Fatalf("response leaked error detail: %s", body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Unauthorized: Invalid token") {
		t.Fatalf("body = %q", body)
	}
}
