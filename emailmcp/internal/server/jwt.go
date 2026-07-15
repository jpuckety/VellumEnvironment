package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"google.golang.org/api/idtoken"
)

const (
	// accessTokenTTL is the lifetime of the short-lived session JWT issued to
	// MCP clients after Google sign-in.
	accessTokenTTL = time.Hour
	// refreshTokenTTL is the lifetime of the refresh token paired with the
	// session JWT for longer-lived access.
	refreshTokenTTL = 7 * 24 * time.Hour
)

// SessionClaims are the claims carried by the session JWT issued to MCP clients.
type SessionClaims struct {
	jwt.Claims
	Email string `json:"email,omitempty"`
	// GoogleIDToken is the Google-issued ID token used for downstream Config API
	// authentication. It is embedded so the resource server can forward a genuine
	// Google token without keeping server-side session state.
	GoogleIDToken string `json:"gid_token,omitempty"`
}

// TokenIssuer mints and verifies the server's own HS256 session JWTs.
type TokenIssuer struct {
	issuer string
	key    []byte
	signer jose.Signer
	ttl    time.Duration
}

// NewTokenIssuer creates a TokenIssuer signing with HS256. When secret is empty
// a random key is generated (issued sessions will not survive a restart).
func NewTokenIssuer(secret []byte, issuer string, ttl time.Duration) (*TokenIssuer, error) {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate jwt secret: %w", err)
		}
	}
	// HS256 requires a 256-bit key. Derive one deterministically from the
	// provided secret so any secret length (including short/dev values) works.
	key := deriveHMACKey(secret)
	if ttl <= 0 {
		ttl = accessTokenTTL
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.HS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, fmt.Errorf("create jwt signer: %w", err)
	}
	return &TokenIssuer{issuer: issuer, key: key, signer: signer, ttl: ttl}, nil
}

// deriveHMACKey returns a 256-bit key derived from secret for HS256 signing.
func deriveHMACKey(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// TTL returns the lifetime of the session JWTs this issuer mints.
func (ti *TokenIssuer) TTL() time.Duration {
	return ti.ttl
}

// Issue mints a signed session JWT for the given user, embedding the Google ID
// token for downstream authentication. It returns the compact JWT and its expiry.
func (ti *TokenIssuer) Issue(subject, email, googleIDToken string) (string, time.Time, error) {
	now := time.Now()
	expiry := now.Add(ti.ttl)
	claims := SessionClaims{
		Claims: jwt.Claims{
			Issuer:    ti.issuer,
			Subject:   subject,
			Audience:  jwt.Audience{ti.issuer},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(expiry),
		},
		Email:         email,
		GoogleIDToken: googleIDToken,
	}
	token, err := jwt.Signed(ti.signer).Claims(claims).Serialize()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}
	return token, expiry, nil
}

// Verify parses and validates a session JWT and returns its claims.
func (ti *TokenIssuer) Verify(token string) (*SessionClaims, error) {
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.HS256})
	if err != nil {
		return nil, fmt.Errorf("parse jwt: %w", err)
	}
	var claims SessionClaims
	if err := parsed.Claims(ti.key, &claims); err != nil {
		return nil, fmt.Errorf("verify jwt: %w", err)
	}
	// Require issuer, audience (set to issuer at mint time), and time validity.
	if err := claims.Validate(jwt.Expected{
		Issuer:      ti.issuer,
		AnyAudience: jwt.Audience{ti.issuer},
		Time:        time.Now(),
	}); err != nil {
		return nil, fmt.Errorf("invalid jwt claims: %w", err)
	}
	return &claims, nil
}

// verifyGoogleIDToken cryptographically validates a Google-issued ID token
// (signature via Google JWKS, audience, issuer, expiry) and returns subject/email.
func verifyGoogleIDToken(ctx context.Context, rawToken, audience string) (subject, email string, err error) {
	if rawToken == "" {
		return "", "", fmt.Errorf("empty id token")
	}
	if audience == "" {
		return "", "", fmt.Errorf("audience (GOOGLE_CLIENT_ID) is required")
	}
	payload, err := idtoken.Validate(ctx, rawToken, audience)
	if err != nil {
		return "", "", fmt.Errorf("google id token validation failed: %w", err)
	}
	if payload.Subject == "" {
		return "", "", fmt.Errorf("google id token missing sub claim")
	}
	if v, ok := payload.Claims["email_verified"]; ok {
		switch verified := v.(type) {
		case bool:
			if !verified {
				return "", "", fmt.Errorf("google email is not verified")
			}
		case string:
			if verified != "true" {
				return "", "", fmt.Errorf("google email is not verified")
			}
		}
	}
	email, _ = payload.Claims["email"].(string)
	return payload.Subject, email, nil
}
