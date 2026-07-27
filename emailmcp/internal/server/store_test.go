package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemorySessionStore_PutGetDelete(t *testing.T) {
	ctx := context.Background()
	store := newMemorySessionStore()

	sess := &Session{
		AccessToken:      "access-1",
		RefreshToken:     "refresh-1",
		ClientID:         "client-1",
		Subject:          "sub-1",
		Email:            "user@example.com",
		GoogleIDToken:    "google-id-token",
		GoogleRefresh:    "google-refresh",
		AccessExpiresAt:  time.Now().Add(time.Hour),
		RefreshExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := store.PutSession(ctx, sess); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	got, err := store.GetSessionByAccessToken(ctx, "access-1")
	if err != nil {
		t.Fatalf("GetSessionByAccessToken: %v", err)
	}
	if got.Subject != "sub-1" || got.GoogleIDToken != "google-id-token" {
		t.Fatalf("unexpected session: %+v", got)
	}

	byRefresh, err := store.GetSessionByRefreshToken(ctx, "refresh-1")
	if err != nil {
		t.Fatalf("GetSessionByRefreshToken: %v", err)
	}
	if byRefresh.AccessToken != "access-1" {
		t.Fatalf("refresh lookup returned wrong access token: %q", byRefresh.AccessToken)
	}

	if err := store.DeleteSession(ctx, "access-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := store.GetSessionByAccessToken(ctx, "access-1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound after delete, got %v", err)
	}
	if _, err := store.GetSessionByRefreshToken(ctx, "refresh-1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected refresh mapping removed after delete, got %v", err)
	}
}

// TestMemorySessionStore_RotationKeepsRefresh mirrors the token-refresh flow:
// a new session (rotated access token, same refresh token) is stored, then the
// old access-token item is deleted. The refresh token must still resolve to the
// NEW access token.
func TestMemorySessionStore_RotationKeepsRefresh(t *testing.T) {
	ctx := context.Background()
	store := newMemorySessionStore()

	old := &Session{
		AccessToken:      "access-old",
		RefreshToken:     "refresh-1",
		AccessExpiresAt:  time.Now().Add(time.Hour),
		RefreshExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := store.PutSession(ctx, old); err != nil {
		t.Fatalf("PutSession(old): %v", err)
	}

	rotated := &Session{
		AccessToken:      "access-new",
		RefreshToken:     "refresh-1",
		AccessExpiresAt:  time.Now().Add(time.Hour),
		RefreshExpiresAt: old.RefreshExpiresAt,
	}
	if err := store.PutSession(ctx, rotated); err != nil {
		t.Fatalf("PutSession(rotated): %v", err)
	}
	if err := store.DeleteSession(ctx, "access-old"); err != nil {
		t.Fatalf("DeleteSession(old): %v", err)
	}

	got, err := store.GetSessionByRefreshToken(ctx, "refresh-1")
	if err != nil {
		t.Fatalf("GetSessionByRefreshToken after rotation: %v", err)
	}
	if got.AccessToken != "access-new" {
		t.Fatalf("refresh should map to rotated access token, got %q", got.AccessToken)
	}
	if _, err := store.GetSessionByAccessToken(ctx, "access-old"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old access token should be gone, got %v", err)
	}
}

func TestMemorySessionStore_Clients(t *testing.T) {
	ctx := context.Background()
	store := newMemorySessionStore()

	if _, err := store.GetClient(ctx, "missing"); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("expected ErrClientNotFound, got %v", err)
	}

	c := &ClientRegistration{
		ClientID:     "client-1",
		RedirectURIs: []string{"http://127.0.0.1:9999/cb"},
		ClientName:   "Test",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := store.PutClient(ctx, c); err != nil {
		t.Fatalf("PutClient: %v", err)
	}
	got, err := store.GetClient(ctx, "client-1")
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "http://127.0.0.1:9999/cb" {
		t.Fatalf("unexpected client: %+v", got)
	}
	// Mutating the returned slice must not affect stored state (defensive copy).
	got.RedirectURIs[0] = "mutated"
	again, _ := store.GetClient(ctx, "client-1")
	if again.RedirectURIs[0] != "http://127.0.0.1:9999/cb" {
		t.Fatalf("stored client mutated via returned slice: %+v", again)
	}
}
