package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
	tokens              *TokenIssuer
	publicBaseURL       string
	resourceMetadataURL string
}

// NewAuthenticator builds the resource-server middleware that verifies the
// server's own session JWTs (issued after Google sign-in) using tokens.
func NewAuthenticator(tokens *TokenIssuer, publicBaseURL string) *Authenticator {
	base := strings.TrimRight(publicBaseURL, "/")
	resourceMeta := ""
	if base != "" {
		resourceMeta = base + "/.well-known/oauth-protected-resource"
	}

	return &Authenticator{
		tokens:              tokens,
		publicBaseURL:       base,
		resourceMetadataURL: resourceMeta,
	}
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
		claims, err := a.tokens.Verify(token)
		if err != nil {
			a.writeUnauthorized(w, r, "Unauthorized: Invalid token: "+err.Error())
			return
		}

		userInfo := &UserInfo{
			Subject: claims.Subject,
			Email:   claims.Email,
		}

		ctx := context.WithValue(r.Context(), userContextKey, userInfo)
		// Forward the embedded Google ID token downstream: the Config API expects a
		// genuine Google token, not our session JWT. Fall back to the raw bearer
		// token if none was embedded.
		downstreamToken := claims.GoogleIDToken
		if downstreamToken == "" {
			downstreamToken = token
		}
		ctx = context.WithValue(ctx, tokenContextKey, downstreamToken)
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
