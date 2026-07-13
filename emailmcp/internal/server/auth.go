package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

type contextKey string

const (
	userContextKey  contextKey = "user"
	tokenContextKey contextKey = "token"
)

type UserInfo struct {
	Subject string
	Email   string
}

type Authenticator struct {
	verifier *oidc.IDTokenVerifier
}

func NewAuthenticator(ctx context.Context, clientID string) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, err
	}

	config := &oidc.Config{
		ClientID: clientID,
	}
	// If ClientID is empty, it will only verify the signature and issuer.
	if clientID == "" {
		config.SkipClientIDCheck = true
	}
	verifier := provider.Verifier(config)

	return &Authenticator{verifier: verifier}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized: Missing Authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			http.Error(w, "Unauthorized: Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		token := parts[1]
		idToken, err := a.verifier.Verify(r.Context(), token)
		if err != nil {
			http.Error(w, "Unauthorized: Invalid token: "+err.Error(), http.StatusUnauthorized)
			return
		}

		var claims struct {
			Email string `json:"email"`
		}
		if err := idToken.Claims(&claims); err != nil {
			http.Error(w, "Unauthorized: Failed to parse claims", http.StatusUnauthorized)
			return
		}

		userInfo := &UserInfo{
			Subject: idToken.Subject,
			Email:   claims.Email,
		}

		ctx := context.WithValue(r.Context(), userContextKey, userInfo)
		ctx = context.WithValue(ctx, tokenContextKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserFromContext(ctx context.Context) (*UserInfo, bool) {
	u, ok := ctx.Value(userContextKey).(*UserInfo)
	return u, ok
}

func TokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tokenContextKey).(string)
	return t, ok
}
