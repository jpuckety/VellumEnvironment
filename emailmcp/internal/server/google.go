package server

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/api/idtoken"
)

// verifyGoogleIDToken cryptographically validates a Google-issued ID token
// (signature via Google JWKS, audience, issuer, expiry) and returns the
// subject, email, and token expiry.
func verifyGoogleIDToken(ctx context.Context, rawToken, audience string) (subject, email string, expiry time.Time, err error) {
	if rawToken == "" {
		return "", "", time.Time{}, fmt.Errorf("empty id token")
	}
	if audience == "" {
		return "", "", time.Time{}, fmt.Errorf("audience (GOOGLE_CLIENT_ID) is required")
	}
	payload, err := idtoken.Validate(ctx, rawToken, audience)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("google id token validation failed: %w", err)
	}
	if payload.Subject == "" {
		return "", "", time.Time{}, fmt.Errorf("google id token missing sub claim")
	}
	if v, ok := payload.Claims["email_verified"]; ok {
		switch verified := v.(type) {
		case bool:
			if !verified {
				return "", "", time.Time{}, fmt.Errorf("google email is not verified")
			}
		case string:
			if verified != "true" {
				return "", "", time.Time{}, fmt.Errorf("google email is not verified")
			}
		}
	}
	email, _ = payload.Claims["email"].(string)
	if payload.Expires > 0 {
		expiry = time.Unix(payload.Expires, 0)
	}
	return payload.Subject, email, expiry, nil
}
