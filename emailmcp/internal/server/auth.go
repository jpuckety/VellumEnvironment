package server

import (
	"context"
	"fmt"
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
	verifier            *oidc.IDTokenVerifier
	publicBaseURL       string
	resourceMetadataURL string
}

func NewAuthenticator(ctx context.Context, clientID, publicBaseURL string) (*Authenticator, error) {
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

	base := strings.TrimRight(publicBaseURL, "/")
	resourceMeta := ""
	if base != "" {
		resourceMeta = base + "/.well-known/oauth-protected-resource"
	}

	return &Authenticator{
		verifier:            verifier,
		publicBaseURL:       base,
		resourceMetadataURL: resourceMeta,
	}, nil
}

func (a *Authenticator) writeUnauthorized(w http.ResponseWriter, r *http.Request, msg string) {
	if a.resourceMetadataURL != "" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Bearer realm="mcp", resource_metadata=%q`,
			a.resourceMetadataURL,
		))
	}

	// Browsers navigating the root URL get a short HTML page instead of plain 401 text.
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/html") && !strings.Contains(accept, "application/json") {
		displayURL := a.publicBaseURL
		if displayURL == "" {
			displayURL = "https://emailmcp.example"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><title>EmailMCP</title>
<style>
  body{font-family:system-ui,sans-serif;max-width:40rem;margin:3rem auto;padding:0 1rem;line-height:1.5;color:#1a1a1a}
  code{background:#f4f4f4;padding:.15em .35em;border-radius:4px}
  a{color:#1a73e8}
</style>
</head>
<body>
  <h1>EmailMCP</h1>
  <p>This is a <strong>Model Context Protocol (MCP)</strong> server, not a traditional web app.</p>
  <p>Connect with an MCP client (Claude, Cursor, VS Code, etc.) using:</p>
  <p><code>%s</code></p>
  <p>When the client connects without credentials, it will discover OAuth metadata and open Google sign-in for you.</p>
  <p style="color:#666;font-size:.9rem">%s</p>
</body>
</html>`, htmlEscape(displayURL), htmlEscape(msg))
		return
	}

	http.Error(w, msg, http.StatusUnauthorized)
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(s)
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			a.writeUnauthorized(w, r, "Unauthorized: Missing Authorization header")
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
			a.writeUnauthorized(w, r, "Unauthorized: Invalid Authorization header format")
			return
		}

		token := parts[1]
		idToken, err := a.verifier.Verify(r.Context(), token)
		if err != nil {
			a.writeUnauthorized(w, r, "Unauthorized: Invalid token: "+err.Error())
			return
		}

		var claims struct {
			Email string `json:"email"`
		}
		if err := idToken.Claims(&claims); err != nil {
			a.writeUnauthorized(w, r, "Unauthorized: Failed to parse claims")
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
