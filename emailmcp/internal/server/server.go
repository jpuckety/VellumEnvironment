package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jpuckett/EmailMCP/emailmcp/internal/config"
	imapmgr "github.com/jpuckett/EmailMCP/emailmcp/internal/imap"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/netutil"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/smtp"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

// Server wraps the MCP server and dependencies.
type Server struct {
	mcpServer     *mcp.Server
	imapMgr       *imapmgr.Manager
	smtpSend      *smtp.Sender
	logger        *slog.Logger
	cfg           *config.Config
	authenticator *Authenticator
	oauth         *OAuthServer
	configClient  *config.Client
}

// New creates and configures the EmailMCP server.
func New(ctx context.Context, cfg *config.Config) (*Server, error) {
	if cfg.ConfigAPIURL == "" {
		return nil, errors.New("CONFIG_API_URL is required")
	}
	if cfg.GoogleClientID == "" {
		return nil, errors.New("GOOGLE_CLIENT_ID is required")
	}

	imapCfg := imapmgr.Config{
		MaxConnsPerAccount: cfg.IMAPMaxConnsPerAccount,
		IdleTimeout:        cfg.IMAPConnIdleTimeout,
		Logger:             slog.Default(),
	}

	imapMgr := imapmgr.NewManager(imapCfg)
	smtpSender := smtp.NewSender(smtp.Config{
		DefaultTimeout: cfg.SMTPDefaultTimeout,
		Logger:         slog.Default(),
	})

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "emailmcp",
		Version: "0.1.0",
	}, nil)

	logger := slog.Default()

	// The token issuer signs and verifies the server's own session JWTs. It is
	// shared by the OAuth server (which mints them) and the authenticator (which
	// verifies them), so both must use the same signing key.
	tokens, err := NewTokenIssuer([]byte(cfg.JWTSecret), cfg.PublicBaseURL, accessTokenTTL)
	if err != nil {
		return nil, fmt.Errorf("failed to create token issuer: %w", err)
	}
	if cfg.JWTSecret == "" {
		logger.Warn("EMAILMCP_JWT_SECRET not set; using a random signing key (issued sessions will not survive a restart or span multiple instances)")
	}

	// OAuth (HTTP mode) needs a public base URL and Google client secret so MCP
	// clients can complete the authorization code + PKCE flow via Google.
	var oauth *OAuthServer
	if cfg.Transport == "" || cfg.Transport == "http" {
		if cfg.PublicBaseURL == "" {
			return nil, errors.New("PUBLIC_BASE_URL is required for HTTP mode (e.g. https://emailmcp.ecg.co)")
		}
		if cfg.GoogleClientSecret == "" {
			return nil, errors.New("GOOGLE_CLIENT_SECRET is required for HTTP mode OAuth")
		}
		oauth, err = NewOAuthServer(cfg.PublicBaseURL, cfg.GoogleClientID, cfg.GoogleClientSecret, tokens, logger, cfg.OAuthRedirectAllowlist)
		if err != nil {
			return nil, fmt.Errorf("failed to create oauth server: %w", err)
		}
		if len(cfg.OAuthRedirectAllowlist) > 0 {
			logger.Info("oauth HTTPS redirect allowlist enforced",
				"entries", len(cfg.OAuthRedirectAllowlist),
			)
		} else {
			logger.Warn("oauth HTTPS redirect allowlist empty; any HTTPS redirect_uri is accepted (set OAUTH_REDIRECT_ALLOWLIST to enforce)")
		}
	}

	auth := NewAuthenticator(tokens, cfg.PublicBaseURL)

	configClient := config.NewClient(cfg.ConfigAPIURL, cfg.ApplicationID)

	es := &Server{
		mcpServer:     srv,
		imapMgr:       imapMgr,
		smtpSend:      smtpSender,
		logger:        logger,
		cfg:           cfg,
		authenticator: auth,
		oauth:         oauth,
		configClient:  configClient,
	}

	es.logger.Info("initializing EmailMCP server", "name", "emailmcp", "version", "0.1.0")
	if oauth != nil {
		es.logger.Info("oauth authorization server enabled",
			"issuer", cfg.PublicBaseURL,
			"callback", cfg.PublicBaseURL+"/oauth/callback",
		)
	}
	es.registerTools()
	es.logger.Info("mcp server initialized")

	return es, nil
}

// HTTPHandler returns the Streamable HTTP handler wrapped with request/response logging and authentication.
// It includes an unsecured /health endpoint for load balancer liveness probes and MCP OAuth endpoints.
func (s *Server) HTTPHandler() http.Handler {
	mux := http.NewServeMux()

	// Health check endpoint (unsecured)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// MCP OAuth discovery + authorize/token/register (unsecured).
	if s.oauth != nil {
		s.oauth.Mount(mux)
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return s.mcpServer
	}, &mcp.StreamableHTTPOptions{
		Logger: s.logger,
	})

	// MCP handler requires Google ID token authentication.
	handler := s.authenticator.Middleware(mcpHandler)

	// Register the MCP handler at the root.
	mux.Handle("/", handler)

	// Wrap everything in logging.
	return httpLogging(s.logger, mux)
}

// ServeStdio runs the MCP server on the stdio transport.
// Note: stdio has no HTTP auth middleware; tool handlers still require a user
// context and will fail unless the transport is extended to supply tokens.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.mcpServer.Run(ctx, &mcp.StdioTransport{})
}

// httpLogging logs every incoming HTTP request/response at Debug level. It does
// NOT log bodies because MCP payloads may contain email content or account
// credentials (see the project's Security Rules in agents.md).
func httpLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		isSSE := r.Header.Get("Accept") == "text/event-stream"
		level := slog.LevelDebug
		if isSSE {
			level = slog.LevelInfo
		}

		// Redact sensitive headers before logging.
		hdrs := make(map[string]string, len(r.Header))
		for k, v := range r.Header {
			switch strings.ToLower(k) {
			case "authorization", "cookie", "proxy-authorization":
				hdrs[k] = "[REDACTED]"
			default:
				hdrs[k] = strings.Join(v, ",")
			}
		}

		logger.Log(r.Context(), level, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"remote", r.RemoteAddr,
			"proto", r.Proto,
			"host", r.Host,
			"content_type", r.Header.Get("Content-Type"),
			"accept", r.Header.Get("Accept"),
			"mcp_session_id", r.Header.Get("Mcp-Session-Id"),
			"mcp_protocol_version", r.Header.Get("MCP-Protocol-Version"),
			"headers", hdrs,
		)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		logger.Log(r.Context(), level, "http response",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			"duration_ms", time.Since(start).Milliseconds(),
			"resp_content_type", rec.Header().Get("Content-Type"),
			"resp_mcp_session_id", rec.Header().Get("Mcp-Session-Id"),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)
	return n, err
}

// Flush exposes the underlying Flusher so the SDK's streamable transport can
// push SSE events without buffering.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) registerTools() {
	// Account management (Config API)
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "add_email_account",
		Description: "Create or replace the authenticated user's email account (IMAP + SMTP). Credentials are stored via the Config API (DynamoDB + Secrets Manager).",
	}, s.addEmailAccount)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_email_accounts",
		Description: "List the authenticated user's configured email accounts (without credentials).",
	}, s.listEmailAccounts)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "remove_email_account",
		Description: "Remove the authenticated user's email account configuration.",
	}, s.removeEmailAccount)

	// IMAP tools
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_folders",
		Description: "List IMAP folders/mailboxes for an account.",
	}, s.listFolders)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name: "search_emails",
		Description: "Search emails in a folder and return summaries. " +
			"Supports basic criteria (query, from, since). " +
			"Use the optional 'limit' parameter to control how many messages are returned (default 50). " +
			"Results are sorted by newest internal date (arrival) by default; " +
			"override with sort_by (arrival, date, from, to, cc, subject, size) and optional sort_reverse.",
	}, s.searchEmails)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "read_email",
		Description: "Read a single email by UID including text/HTML body and attachment metadata.",
	}, s.readEmail)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "move_emails",
		Description: "Move one or more emails (by UID) to another folder.",
	}, s.moveEmails)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "flag_emails",
		Description: "Add or remove flags (e.g. \\Seen, \\Flagged, \\Deleted) on emails.",
	}, s.flagEmails)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "delete_emails",
		Description: "Delete one or more emails by marking them \\Deleted.",
	}, s.deleteEmails)

	// SMTP
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "send_email",
		Description: "Send an email (supports plain text, HTML, and attachments as base64).",
	}, s.sendEmail)
}

// --- Input structs (for schema) ---

type AddAccountInput struct {
	ID           string `json:"id,omitempty" jsonschema:"Optional unique ID for the account; auto-generated from name if empty"`
	Name         string `json:"name" jsonschema:"Human friendly name for the account"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port" jsonschema:"default:993"`
	IMAPUsername string `json:"imap_username"`
	IMAPPassword string `json:"imap_password" jsonschema:"The IMAP password (stored in Secrets Manager)"`
	// Pointer so JSON omission honors default true (zero-value bool would be false).
	IMAPUseTLS *bool `json:"imap_use_tls,omitempty" jsonschema:"default:true"`

	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port" jsonschema:"default:587"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password" jsonschema:"The SMTP password; defaults to IMAP password when empty"`
	SMTPUseTLS   *bool  `json:"smtp_use_tls,omitempty" jsonschema:"default:true"`

	FromAddress string `json:"from_address,omitempty"`
}

type AccountIDInput struct {
	AccountID string `json:"account_id" jsonschema:"Account ID"`
}

type ListFoldersInput struct {
	AccountID string `json:"account_id"`
}

type SearchEmailsInput struct {
	AccountID string `json:"account_id"`
	Folder    string `json:"folder,omitempty" jsonschema:"Folder to search, defaults to INBOX"`
	Query     string `json:"query,omitempty" jsonschema:"Simple text search in subject/body"`
	From      string `json:"from,omitempty"`
	Since     string `json:"since,omitempty" jsonschema:"Date in RFC3339 or YYYY-MM-DD"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum number of message summaries to return (default 50)"`
	// SortBy is one of: arrival (internal date, default), date, from, to, cc, subject, size.
	SortBy string `json:"sort_by,omitempty" jsonschema:"Sort key: arrival (internal date, default), date, from, to, cc, subject, size"`
	// SortReverse overrides default direction. Defaults to true for arrival/date/size, false for string keys.
	SortReverse *bool `json:"sort_reverse,omitempty" jsonschema:"Reverse sort order. Defaults to true for arrival/date/size (newest/largest first), false for from/to/cc/subject"`
}

type ReadEmailInput struct {
	AccountID string `json:"account_id"`
	Folder    string `json:"folder,omitempty"`
	UID       uint32 `json:"uid"`
}

type MoveEmailsInput struct {
	AccountID  string   `json:"account_id"`
	Folder     string   `json:"folder,omitempty"`
	UIDs       []uint32 `json:"uids"`
	DestFolder string   `json:"dest_folder"`
}

type FlagEmailsInput struct {
	AccountID string   `json:"account_id"`
	Folder    string   `json:"folder,omitempty"`
	UIDs      []uint32 `json:"uids"`
	Flags     []string `json:"flags"`
	Add       bool     `json:"add" jsonschema:"true to add flags, false to remove"`
}

type DeleteEmailsInput struct {
	AccountID string   `json:"account_id"`
	Folder    string   `json:"folder,omitempty"`
	UIDs      []uint32 `json:"uids"`
}

type SendEmailToolInput struct {
	AccountID   string                  `json:"account_id"`
	To          []string                `json:"to"`
	Cc          []string                `json:"cc,omitempty"`
	Bcc         []string                `json:"bcc,omitempty"`
	Subject     string                  `json:"subject"`
	Text        string                  `json:"text,omitempty"`
	HTML        string                  `json:"html,omitempty"`
	From        string                  `json:"from,omitempty"`
	Attachments []types.AttachmentInput `json:"attachments,omitempty"`
}

// --- Handlers ---

func (s *Server) addEmailAccount(ctx context.Context, req *mcp.CallToolRequest, in AddAccountInput) (*mcp.CallToolResult, any, error) {
	user, token, err := s.requireAuth(ctx)
	if err != nil {
		return nil, nil, err
	}
	if in.Name == "" || in.IMAPHost == "" || in.IMAPUsername == "" || in.SMTPHost == "" {
		return nil, nil, errors.New("required fields missing: name, imap_host, imap_username, smtp_host")
	}
	if in.IMAPPassword == "" {
		return nil, nil, errors.New("imap_password is required")
	}

	imapUseTLS := boolDefault(in.IMAPUseTLS, true)
	smtpUseTLS := boolDefault(in.SMTPUseTLS, true)

	if err := netutil.ValidatePublicHost(in.IMAPHost); err != nil {
		return nil, nil, fmt.Errorf("imap_host not allowed: %w", err)
	}
	if err := netutil.ValidatePublicHost(in.SMTPHost); err != nil {
		return nil, nil, fmt.Errorf("smtp_host not allowed: %w", err)
	}
	if err := netutil.RequireTLSUnlessLocalhost(in.IMAPHost, imapUseTLS, "IMAP"); err != nil {
		return nil, nil, err
	}
	if err := netutil.RequireTLSUnlessLocalhost(in.SMTPHost, smtpUseTLS, "SMTP"); err != nil {
		return nil, nil, err
	}

	smtpUser := in.SMTPUsername
	if smtpUser == "" {
		smtpUser = in.IMAPUsername
	}
	smtpPass := in.SMTPPassword
	if smtpPass == "" {
		smtpPass = in.IMAPPassword
	}

	// Generate a slug from name if ID is not provided
	accountID := in.ID
	if accountID == "" {
		accountID = strings.ToLower(strings.ReplaceAll(in.Name, " ", "-"))
	}
	if accountID == "" {
		accountID = "default"
	}

	acc := &types.Account{
		ID:           accountID,
		OwnerUserID:  user.Subject,
		Name:         in.Name,
		IMAPHost:     in.IMAPHost,
		IMAPPort:     defaultPort(in.IMAPPort, 993),
		IMAPUsername: in.IMAPUsername,
		IMAPPassword: in.IMAPPassword,
		IMAPUseTLS:   imapUseTLS,
		SMTPHost:     in.SMTPHost,
		SMTPPort:     defaultPort(in.SMTPPort, 587),
		SMTPUsername: smtpUser,
		SMTPPassword: smtpPass,
		SMTPUseTLS:   smtpUseTLS,
		FromAddress:  in.FromAddress,
	}

	if err := s.configClient.PutUserConfig(ctx, token, user.Subject, acc); err != nil {
		return nil, nil, fmt.Errorf("store account via config api: %w", err)
	}

	// Drop any existing pool so credential/host changes take effect immediately.
	if s.imapMgr != nil {
		s.imapMgr.DropPool(user.Subject, acc.ID)
	}

	s.logger.Info("account added", "id", acc.ID, "name", acc.Name, "owner", user.Subject)

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Account saved successfully. ID: %s", acc.ID)}},
	}, map[string]any{"id": acc.ID}, nil
}

func (s *Server) listEmailAccounts(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	user, token, err := s.requireAuth(ctx)
	if err != nil {
		return nil, nil, err
	}

	accs, err := s.configClient.ListUserConfigs(ctx, token, user.Subject)
	if err != nil {
		return nil, nil, err
	}

	summaries := make([]types.AccountSummary, 0, len(accs))
	for _, acc := range accs {
		summaries = append(summaries, types.AccountSummary{
			ID:          acc.ID,
			Name:        acc.Name,
			IMAPHost:    acc.IMAPHost,
			IMAPPort:    acc.IMAPPort,
			SMTPHost:    acc.SMTPHost,
			SMTPPort:    acc.SMTPPort,
			FromAddress: acc.FromAddress,
			CreatedAt:   acc.CreatedAt,
			UpdatedAt:   acc.UpdatedAt,
		})
	}
	return nil, map[string]any{"accounts": summaries}, nil
}

func (s *Server) removeEmailAccount(ctx context.Context, req *mcp.CallToolRequest, in AccountIDInput) (*mcp.CallToolResult, any, error) {
	user, token, err := s.requireAuth(ctx)
	if err != nil {
		return nil, nil, err
	}
	if in.AccountID == "" {
		return nil, nil, errors.New("account_id is required")
	}

	if err := s.configClient.DeleteUserConfig(ctx, token, user.Subject, in.AccountID); err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return nil, nil, errors.New("account not found")
		}
		return nil, nil, err
	}
	// Close any live IMAP connections for this tenant account.
	if s.imapMgr != nil {
		s.imapMgr.DropPool(user.Subject, in.AccountID)
	}
	s.logger.Info("account removed", "id", in.AccountID, "owner", user.Subject)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Account removed"}}}, nil, nil
}

func (s *Server) listFolders(ctx context.Context, req *mcp.CallToolRequest, in ListFoldersInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}
	folders, err := s.imapMgr.ListFolders(ctx, acc)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"folders": folders}, nil
}

func (s *Server) searchEmails(ctx context.Context, req *mcp.CallToolRequest, in SearchEmailsInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}

	crit := imap.SearchCriteria{}
	if in.Query != "" {
		crit.Text = []string{in.Query}
	}
	if in.From != "" {
		crit.Header = append(crit.Header, imap.SearchCriteriaHeaderField{Key: "From", Value: in.From})
	}
	if in.Since != "" {
		if t, err := parseDate(in.Since); err == nil {
			crit.Since = t
		}
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}

	sortKey, err := imapmgr.ParseSearchSortKey(in.SortBy)
	if err != nil {
		return nil, nil, err
	}

	folder := in.Folder
	summaries, err := s.imapMgr.SearchEmails(ctx, acc, folder, crit, limit, imapmgr.SearchSort{
		Key:     sortKey,
		Reverse: in.SortReverse,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"emails": summaries}, nil
}

func (s *Server) readEmail(ctx context.Context, req *mcp.CallToolRequest, in ReadEmailInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}
	msg, err := s.imapMgr.GetEmail(ctx, acc, in.Folder, in.UID)
	if err != nil {
		return nil, nil, err
	}
	return nil, msg, nil
}

func (s *Server) moveEmails(ctx context.Context, req *mcp.CallToolRequest, in MoveEmailsInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.imapMgr.MoveEmails(ctx, acc, in.Folder, in.UIDs, in.DestFolder); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Emails moved"}}}, nil, nil
}

func (s *Server) flagEmails(ctx context.Context, req *mcp.CallToolRequest, in FlagEmailsInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.imapMgr.FlagEmails(ctx, acc, in.Folder, in.UIDs, in.Flags, in.Add); err != nil {
		return nil, nil, err
	}
	action := "added"
	if !in.Add {
		action = "removed"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Flags %s", action)}}}, nil, nil
}

func (s *Server) deleteEmails(ctx context.Context, req *mcp.CallToolRequest, in DeleteEmailsInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if err := s.imapMgr.DeleteEmails(ctx, acc, in.Folder, in.UIDs); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Emails marked for deletion"}}}, nil, nil
}

func (s *Server) sendEmail(ctx context.Context, req *mcp.CallToolRequest, in SendEmailToolInput) (*mcp.CallToolResult, any, error) {
	acc, err := s.getAccount(ctx, in.AccountID)
	if err != nil {
		return nil, nil, err
	}

	sendInput := types.SendEmailInput{
		To:          in.To,
		Cc:          in.Cc,
		Bcc:         in.Bcc,
		Subject:     in.Subject,
		Text:        in.Text,
		HTML:        in.HTML,
		From:        in.From,
		Attachments: in.Attachments,
	}

	if err := s.smtpSend.SendEmail(ctx, acc, sendInput); err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Email sent successfully"}}}, nil, nil
}

// --- Helpers ---

func (s *Server) requireAuth(ctx context.Context) (*UserInfo, string, error) {
	userInfo, ok := UserFromContext(ctx)
	if !ok {
		return nil, "", errors.New("authentication required: provide a valid session token")
	}
	token, ok := TokenFromContext(ctx)
	if !ok || token == "" {
		return nil, "", errors.New("authentication token required")
	}
	return userInfo, token, nil
}

// getAccount loads an authenticated user's email account from the Config API.
func (s *Server) getAccount(ctx context.Context, accountID string) (*types.Account, error) {
	userInfo, token, err := s.requireAuth(ctx)
	if err != nil {
		return nil, err
	}

	if accountID == "" {
		return nil, errors.New("account_id is required")
	}

	s.logger.Debug("fetching config from API", "user", userInfo.Email, "account_id", accountID)
	accs, err := s.configClient.GetUserConfig(ctx, token, userInfo.Subject, accountID)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return nil, fmt.Errorf("account %q not found; call add_email_account first", accountID)
		}
		return nil, fmt.Errorf("fetch config from api: %w", err)
	}

	if len(accs) == 0 {
		return nil, fmt.Errorf("account %q not found; call add_email_account first", accountID)
	}

	// Try to find the exact match by ID if multiple are returned.
	for _, acc := range accs {
		if acc.ID == accountID {
			// Bind ownership for pool isolation and re-validate host/TLS policy.
			acc.OwnerUserID = userInfo.Subject
			if err := validateAccountEndpoints(acc); err != nil {
				return nil, err
			}
			return acc, nil
		}
	}

	return nil, fmt.Errorf("account %q not found; call add_email_account first", accountID)
}

// validateAccountEndpoints enforces SSRF and TLS policy on stored config before dial.
func validateAccountEndpoints(acc *types.Account) error {
	if err := netutil.ValidatePublicHost(acc.IMAPHost); err != nil {
		return fmt.Errorf("imap_host not allowed: %w", err)
	}
	if err := netutil.ValidatePublicHost(acc.SMTPHost); err != nil {
		return fmt.Errorf("smtp_host not allowed: %w", err)
	}
	if err := netutil.RequireTLSUnlessLocalhost(acc.IMAPHost, acc.IMAPUseTLS, "IMAP"); err != nil {
		return err
	}
	if err := netutil.RequireTLSUnlessLocalhost(acc.SMTPHost, acc.SMTPUseTLS, "SMTP"); err != nil {
		return err
	}
	return nil
}

func defaultPort(p, def int) int {
	if p > 0 {
		return p
	}
	return def
}

// boolDefault returns *b when set, otherwise def. Used so omitted JSON bools
// honor schema defaults (e.g. imap_use_tls / smtp_use_tls default true).
func boolDefault(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

func parseDate(s string) (time.Time, error) {
	layouts := []string{time.RFC3339, "2006-01-02", time.DateOnly}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized date format: %s", s)
}
