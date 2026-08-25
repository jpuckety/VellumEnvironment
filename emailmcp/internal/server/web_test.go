package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/config"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
	"golang.org/x/oauth2"
)

func newTestWebServerWithSession(t *testing.T) (*Server, string) {
	t.Helper()
	cfg := &config.Config{
		Transport:          "http",
		PublicBaseURL:      "https://emailmcp.ecg.co",
		GoogleClientID:     "test-client-id",
		GoogleClientSecret: "test-client-secret",
		ApplicationID:      "emailmcp",
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	token := "test-web-access-token"
	sess := &Session{
		AccessToken:      token,
		RefreshToken:     "test-web-refresh-token",
		Subject:          "user123",
		Email:            "user123@example.com",
		AccessExpiresAt:  time.Now().Add(time.Hour),
		RefreshExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := srv.authenticator.store.PutSession(context.Background(), sess); err != nil {
		t.Fatalf("PutSession failed: %v", err)
	}
	return srv, token
}

func TestWebStatic_ServesIndexAndSPA(t *testing.T) {
	srv, _ := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	// 1. User Portal root path GET /portal
	resp, err := http.Get(ts.URL + "/portal")
	if err != nil {
		t.Fatalf("GET /portal: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %s, want text/html", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "app-root") {
		t.Errorf("expected body to contain 'app-root', got: %s", string(body))
	}
	if !strings.Contains(string(body), `<base href="/portal/">`) {
		t.Errorf("expected body to contain base href /portal/, got: %s", string(body))
	}

	// 2. User Portal trailing slash path GET /portal/
	respSlash, err := http.Get(ts.URL + "/portal/")
	if err != nil {
		t.Fatalf("GET /portal/: %v", err)
	}
	bodySlash, _ := io.ReadAll(respSlash.Body)
	respSlash.Body.Close()

	if respSlash.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal/ status = %d, want 200", respSlash.StatusCode)
	}
	if !strings.Contains(string(bodySlash), "app-root") {
		t.Errorf("expected body to contain 'app-root', got: %s", string(bodySlash))
	}

	// 3. SPA Route GET /portal/accounts/new (should fallback to index.html)
	resp2, err := http.Get(ts.URL + "/portal/accounts/new")
	if err != nil {
		t.Fatalf("GET /portal/accounts/new: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal/accounts/new status = %d, want 200", resp2.StatusCode)
	}
	if !strings.Contains(string(body2), "app-root") {
		t.Errorf("expected SPA fallback to index.html, got: %s", string(body2))
	}

	// 4. Root path GET / without MCP auth headers routes to MCP handler (401 Unauthorized)
	respRoot, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	respRoot.Body.Close()
	if respRoot.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET / status = %d, want 401 Unauthorized for MCP root", respRoot.StatusCode)
	}
}

func TestWebCallback_Redirects(t *testing.T) {
	srv, _ := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. Error param redirect
	respErr, err := client.Get(ts.URL + "/auth/callback?error=access_denied")
	if err != nil {
		t.Fatalf("GET /auth/callback?error: %v", err)
	}
	respErr.Body.Close()
	if respErr.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", respErr.StatusCode)
	}
	loc := respErr.Header.Get("Location")
	if loc != "/portal/login?error=access_denied" {
		t.Errorf("Location = %q, want /portal/login?error=access_denied", loc)
	}

	// 2. Missing code/state redirect
	respMissing, err := client.Get(ts.URL + "/auth/callback")
	if err != nil {
		t.Fatalf("GET /auth/callback: %v", err)
	}
	respMissing.Body.Close()
	if respMissing.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", respMissing.StatusCode)
	}
	locMissing := respMissing.Header.Get("Location")
	if locMissing != "/portal/login?error=missing_code_or_state" {
		t.Errorf("Location = %q, want /portal/login?error=missing_code_or_state", locMissing)
	}
}

func TestWebLogin_GoogleCallbackIssuesPortalSession(t *testing.T) {
	srv, _ := newTestWebServerWithSession(t)
	srv.oauth.exchangeFn = func(_ context.Context, code string) (*oauth2.Token, error) {
		if code != "google-code" {
			t.Fatalf("unexpected google code %q", code)
		}
		tok := &oauth2.Token{
			AccessToken:  "g-access",
			RefreshToken: "g-refresh",
			Expiry:       time.Now().Add(time.Hour),
		}
		return tok.WithExtra(map[string]any{"id_token": "fake-id-token"}), nil
	}
	srv.oauth.verifyFn = func(_ context.Context, rawToken, audience string) (string, string, time.Time, error) {
		if rawToken != "fake-id-token" {
			return "", "", time.Time{}, errors.New("bad test token")
		}
		if audience != "test-client-id" {
			t.Fatalf("audience = %q", audience)
		}
		return "sub-1", "user@example.com", time.Now().Add(time.Hour), nil
	}

	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	loginResp, err := client.Get(ts.URL + "/auth/login")
	if err != nil {
		t.Fatalf("GET /auth/login: %v", err)
	}
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusFound {
		t.Fatalf("/auth/login status = %d", loginResp.StatusCode)
	}
	googleURL, err := url.Parse(loginResp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse google location: %v", err)
	}
	if googleURL.Query().Get("redirect_uri") != "https://emailmcp.ecg.co/oauth/callback" {
		t.Fatalf("redirect_uri = %q", googleURL.Query().Get("redirect_uri"))
	}
	state := googleURL.Query().Get("state")
	if state == "" {
		t.Fatal("missing google state")
	}

	cbResp, err := client.Get(ts.URL + "/oauth/callback?code=google-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("GET /oauth/callback: %v", err)
	}
	cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusFound {
		t.Fatalf("/oauth/callback status = %d body=%s", cbResp.StatusCode, "")
	}
	loc := cbResp.Header.Get("Location")
	portalURL, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse portal location %q: %v", loc, err)
	}
	if portalURL.Path != "/portal/" && portalURL.Path != "/portal" {
		t.Fatalf("Location = %q, want /portal/?token=...", loc)
	}
	token := portalURL.Query().Get("token")
	if token == "" {
		t.Fatalf("Location = %q, missing token", loc)
	}
	var sessionCookie string
	for _, c := range cbResp.Cookies() {
		if c.Name == "emailmcp_session" {
			sessionCookie = c.Value
		}
	}
	if sessionCookie != token {
		t.Fatalf("session cookie = %q, token = %q", sessionCookie, token)
	}
	sess, err := srv.authenticator.store.GetSessionByAccessToken(context.Background(), token)
	if err != nil {
		t.Fatalf("session not stored: %v", err)
	}
	if sess.ClientID != webUIClientID || sess.Subject != "sub-1" || sess.Email != "user@example.com" {
		t.Fatalf("session = %+v", sess)
	}
}

func TestAPIMe(t *testing.T) {
	srv, token := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	// 1. Unauthenticated request
	respUnauth, err := http.Get(ts.URL + "/api/me")
	if err != nil {
		t.Fatalf("GET /api/me: %v", err)
	}
	respUnauth.Body.Close()
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", respUnauth.StatusCode)
	}

	// 2. Authenticated via Bearer token
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	respAuth, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/me with auth: %v", err)
	}
	defer respAuth.Body.Close()
	if respAuth.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status = %d, want 200", respAuth.StatusCode)
	}

	var user WebUser
	if err := json.NewDecoder(respAuth.Body).Decode(&user); err != nil {
		t.Fatalf("decode WebUser: %v", err)
	}
	if user.Subject != "user123" || user.Email != "user123@example.com" || !user.Authenticated {
		t.Fatalf("unexpected user: %+v", user)
	}

	// 3. Authenticated via session cookie
	reqCookie, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/me", nil)
	reqCookie.AddCookie(&http.Cookie{Name: "emailmcp_session", Value: token})
	respCookie, err := http.DefaultClient.Do(reqCookie)
	if err != nil {
		t.Fatalf("GET /api/me with cookie: %v", err)
	}
	defer respCookie.Body.Close()
	if respCookie.StatusCode != http.StatusOK {
		t.Fatalf("cookie auth status = %d, want 200", respCookie.StatusCode)
	}
}

func TestAPIAccounts_FullCRUD(t *testing.T) {
	srv, token := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	client := http.DefaultClient

	// 1. Initially empty list
	reqList, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/accounts", nil)
	reqList.Header.Set("Authorization", "Bearer "+token)
	respList, err := client.Do(reqList)
	if err != nil {
		t.Fatalf("GET /api/accounts: %v", err)
	}
	defer respList.Body.Close()
	if respList.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/accounts status = %d, want 200", respList.StatusCode)
	}
	var listRes struct {
		Accounts []types.AccountSummary `json:"accounts"`
	}
	_ = json.NewDecoder(respList.Body).Decode(&listRes)
	if len(listRes.Accounts) != 0 {
		t.Fatalf("expected 0 accounts, got %d", len(listRes.Accounts))
	}

	// 2. Create account (POST /api/accounts)
	useTLS := true
	createPayload := AccountPayload{
		ID:           "work-mail",
		Name:         "Work Fastmail",
		IMAPHost:     "imap.fastmail.com",
		IMAPPort:     993,
		IMAPUsername: "worker@fastmail.com",
		IMAPPassword: "secret-imap-password",
		IMAPUseTLS:   &useTLS,
		SMTPHost:     "smtp.fastmail.com",
		SMTPPort:     587,
		SMTPUsername: "worker@fastmail.com",
		SMTPPassword: "secret-smtp-password",
		SMTPUseTLS:   &useTLS,
		FromAddress:  "Worker <worker@fastmail.com>",
	}
	createJSON, _ := json.Marshal(createPayload)
	reqCreate, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/accounts", bytes.NewReader(createJSON))
	reqCreate.Header.Set("Authorization", "Bearer "+token)
	reqCreate.Header.Set("Content-Type", "application/json")
	respCreate, err := client.Do(reqCreate)
	if err != nil {
		t.Fatalf("POST /api/accounts: %v", err)
	}
	defer respCreate.Body.Close()
	if respCreate.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(respCreate.Body)
		t.Fatalf("POST /api/accounts status = %d (want 201), body = %s", respCreate.StatusCode, string(b))
	}

	var createdAcc AccountResponse
	_ = json.NewDecoder(respCreate.Body).Decode(&createdAcc)
	if createdAcc.ID != "work-mail" || createdAcc.Name != "Work Fastmail" || !createdAcc.HasPassword {
		t.Fatalf("unexpected created account: %+v", createdAcc)
	}

	// 2b. List must return the account without credential material.
	reqListAfter, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/accounts", nil)
	reqListAfter.Header.Set("Authorization", "Bearer "+token)
	respListAfter, err := client.Do(reqListAfter)
	if err != nil {
		t.Fatalf("GET /api/accounts after create: %v", err)
	}
	listBody, _ := io.ReadAll(respListAfter.Body)
	respListAfter.Body.Close()
	if respListAfter.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/accounts after create status = %d, want 200; body = %s", respListAfter.StatusCode, listBody)
	}
	if strings.Contains(string(listBody), "secret-imap-password") || strings.Contains(string(listBody), "secret-smtp-password") {
		t.Fatalf("GET /api/accounts leaked a password: %s", listBody)
	}
	var listed struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(listBody, &listed); err != nil {
		t.Fatalf("decode list after create: %v\nbody = %s", err, listBody)
	}
	if len(listed.Accounts) != 1 {
		t.Fatalf("expected 1 listed account, got %d: %s", len(listed.Accounts), listBody)
	}
	for _, key := range []string{"password", "imap_password", "smtp_password"} {
		if _, ok := listed.Accounts[0][key]; ok {
			t.Fatalf("GET /api/accounts included %q: %s", key, listBody)
		}
	}
	if listed.Accounts[0]["id"] != "work-mail" || listed.Accounts[0]["imap_username"] != "worker@fastmail.com" {
		t.Fatalf("unexpected listed account: %s", listBody)
	}
	if listed.Accounts[0]["has_password"] != true {
		t.Fatalf("expected has_password=true, got %s", listBody)
	}

	// 3. Get single account (GET /api/accounts/work-mail)
	reqGet, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/accounts/work-mail", nil)
	reqGet.Header.Set("Authorization", "Bearer "+token)
	respGet, err := client.Do(reqGet)
	if err != nil {
		t.Fatalf("GET /api/accounts/work-mail: %v", err)
	}
	defer respGet.Body.Close()
	if respGet.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/accounts/work-mail status = %d, want 200", respGet.StatusCode)
	}
	getBody, _ := io.ReadAll(respGet.Body)
	if strings.Contains(string(getBody), "secret-imap-password") || strings.Contains(string(getBody), "secret-smtp-password") {
		t.Fatalf("GET /api/accounts/work-mail leaked a password: %s", getBody)
	}
	var gotAcc AccountResponse
	if err := json.Unmarshal(getBody, &gotAcc); err != nil {
		t.Fatalf("decode got account: %v", err)
	}
	if gotAcc.ID != "work-mail" || gotAcc.IMAPHost != "imap.fastmail.com" || !gotAcc.HasPassword {
		t.Fatalf("unexpected got account: %+v", gotAcc)
	}

	// 4. Update account (PUT /api/accounts/work-mail) - change name, keep password blank
	updatePayload := AccountPayload{
		Name:         "Updated Work Mail",
		IMAPHost:     "imap.fastmail.com",
		IMAPPort:     993,
		IMAPUsername: "worker@fastmail.com",
		IMAPPassword: "", // empty - keeps existing password!
		SMTPHost:     "smtp.fastmail.com",
		SMTPPort:     587,
	}
	updateJSON, _ := json.Marshal(updatePayload)
	reqUpdate, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/accounts/work-mail", bytes.NewReader(updateJSON))
	reqUpdate.Header.Set("Authorization", "Bearer "+token)
	reqUpdate.Header.Set("Content-Type", "application/json")
	respUpdate, err := client.Do(reqUpdate)
	if err != nil {
		t.Fatalf("PUT /api/accounts/work-mail: %v", err)
	}
	defer respUpdate.Body.Close()
	if respUpdate.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(respUpdate.Body)
		t.Fatalf("PUT /api/accounts/work-mail status = %d (want 200), body = %s", respUpdate.StatusCode, string(b))
	}

	// Verify password was preserved in store
	stored, err := srv.configStore.GetUserConfig(context.Background(), "user123", "work-mail")
	if err != nil {
		t.Fatalf("GetUserConfig: %v", err)
	}
	if stored.Name != "Updated Work Mail" {
		t.Errorf("Name = %q, want 'Updated Work Mail'", stored.Name)
	}
	if stored.IMAPPassword != "secret-imap-password" {
		t.Errorf("IMAPPassword was lost: got %q, want 'secret-imap-password'", stored.IMAPPassword)
	}

	// 5. Delete account (DELETE /api/accounts/work-mail)
	reqDel, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/accounts/work-mail", nil)
	reqDel.Header.Set("Authorization", "Bearer "+token)
	respDel, err := client.Do(reqDel)
	if err != nil {
		t.Fatalf("DELETE /api/accounts/work-mail: %v", err)
	}
	defer respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/accounts/work-mail status = %d, want 200", respDel.StatusCode)
	}

	// Confirm it's gone
	_, err = srv.configStore.GetUserConfig(context.Background(), "user123", "work-mail")
	if err == nil {
		t.Fatal("expected error getting deleted account, got nil")
	}
}

func TestAPIAccounts_Validation(t *testing.T) {
	srv, token := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	client := http.DefaultClient

	// SSRF: private host rejection
	useTLS := true
	payload := AccountPayload{
		Name:         "Private Host Account",
		IMAPHost:     "192.168.1.1",
		IMAPPort:     993,
		IMAPUsername: "user@private.com",
		IMAPPassword: "pass",
		IMAPUseTLS:   &useTLS,
		SMTPHost:     "smtp.gmail.com",
		SMTPPort:     587,
		SMTPUseTLS:   &useTLS,
	}
	bodyJSON, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/accounts", bytes.NewReader(bodyJSON))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/accounts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for private host, got %d", resp.StatusCode)
	}
}

func TestAPIAccounts_VerifyRequiresPassword(t *testing.T) {
	srv, token := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	client := http.DefaultClient

	// Verification without password must be rejected
	useTLS := true
	payload := AccountPayload{
		Name:         "Verify Test",
		IMAPHost:     "imap.gmail.com",
		IMAPPort:     993,
		IMAPUsername: "user@gmail.com",
		IMAPPassword: "", // empty!
		IMAPUseTLS:   &useTLS,
		SMTPHost:     "smtp.gmail.com",
		SMTPPort:     587,
		SMTPUseTLS:   &useTLS,
	}
	bodyJSON, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/accounts/verify", bytes.NewReader(bodyJSON))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/accounts/verify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty password verification, got %d", resp.StatusCode)
	}
}

func TestAPILogout(t *testing.T) {
	srv, token := newTestWebServerWithSession(t)
	ts := httptest.NewServer(srv.HTTPHandler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/logout: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Session should be deleted
	_, err = srv.authenticator.store.GetSessionByAccessToken(context.Background(), token)
	if err == nil {
		t.Fatal("expected session to be deleted, got nil error")
	}
}
