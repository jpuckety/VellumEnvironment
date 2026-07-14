package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteUnauthorized_WWWAuthenticate(t *testing.T) {
	a := &Authenticator{
		publicBaseURL:       "https://emailmcp.ecg.co",
		resourceMetadataURL: "https://emailmcp.ecg.co/.well-known/oauth-protected-resource",
	}

	req := httptest.NewRequest(http.MethodGet, "https://emailmcp.ecg.co/", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	a.writeUnauthorized(rec, req, "Unauthorized: Missing Authorization header")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	wa := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(wa, `resource_metadata="https://emailmcp.ecg.co/.well-known/oauth-protected-resource"`) {
		t.Fatalf("WWW-Authenticate = %q", wa)
	}
	if !strings.Contains(rec.Body.String(), "Missing Authorization header") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWriteUnauthorized_HTMLForBrowsers(t *testing.T) {
	a := &Authenticator{
		publicBaseURL:       "https://emailmcp.ecg.co",
		resourceMetadataURL: "https://emailmcp.ecg.co/.well-known/oauth-protected-resource",
	}

	req := httptest.NewRequest(http.MethodGet, "https://emailmcp.ecg.co/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	a.writeUnauthorized(rec, req, "Unauthorized: Missing Authorization header")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("Content-Type = %s", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "Model Context Protocol") {
		t.Fatalf("body missing explanation: %s", rec.Body.String())
	}
	wa := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(wa, "resource_metadata=") {
		t.Fatalf("expected WWW-Authenticate, got %q", wa)
	}
}
