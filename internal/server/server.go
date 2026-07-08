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

	"github.com/jpuckett/EmailMCP/internal/config"
	"github.com/jpuckett/EmailMCP/internal/crypto"
	imapmgr "github.com/jpuckett/EmailMCP/internal/imap"
	"github.com/jpuckett/EmailMCP/internal/smtp"
	"github.com/jpuckett/EmailMCP/internal/store"
	"github.com/jpuckett/EmailMCP/internal/types"
)

// Server wraps the MCP server and dependencies.
type Server struct {
	mcpServer *mcp.Server
	store     *store.Store
	imapMgr   *imapmgr.Manager
	smtpSend  *smtp.Sender
	crypto    *crypto.Service
	logger    *slog.Logger
	cfg       *config.Config
}

// New creates and configures the EmailMCP server.
func New(ctx context.Context, st *store.Store, cryptoSvc *crypto.Service, cfg *config.Config) (*Server, error) {
	if err := config.ValidateMasterKeyPresence(); err != nil {
		return nil, err
	}

	imapCfg := imapmgr.Config{
		MaxConnsPerAccount: cfg.IMAPMaxConnsPerAccount,
		IdleTimeout:        cfg.IMAPConnIdleTimeout,
		Logger:             slog.Default(),
	}

	imapMgr := imapmgr.NewManager(cryptoSvc, imapCfg)
	smtpSender := smtp.NewSender(cryptoSvc, smtp.Config{
		DefaultTimeout: cfg.SMTPDefaultTimeout,
		Logger:         slog.Default(),
	})

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "emailmcp",
		Version: "0.1.0",
	}, nil)

	es := &Server{
		mcpServer: srv,
		store:     st,
		imapMgr:   imapMgr,
		smtpSend:  smtpSender,
		crypto:    cryptoSvc,
		logger:    slog.Default(),
		cfg:       cfg,
	}

	es.registerTools()

	return es, nil
}

// HTTPHandler returns the Streamable HTTP handler wrapped with request/response logging.
func (s *Server) HTTPHandler() http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return s.mcpServer
	}, &mcp.StreamableHTTPOptions{
		Logger: s.logger,
	})
	return httpLogging(s.logger, mcpHandler)
}

// httpLogging logs every incoming HTTP request/response at Debug level. It does
// NOT log bodies because MCP payloads may contain email content or account
// credentials (see the project's Security Rules in agents.md).
func httpLogging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

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

		logger.Debug("http request",
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

		logger.Debug("http response",
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
	// Account management
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "add_email_account",
		Description: "Add a new email account with IMAP and SMTP configuration. Credentials are stored encrypted.",
	}, s.addEmailAccount)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_email_accounts",
		Description: "List all configured email accounts (without credentials).",
	}, s.listEmailAccounts)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "remove_email_account",
		Description: "Remove an email account by ID.",
	}, s.removeEmailAccount)

	// IMAP tools
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "list_folders",
		Description: "List IMAP folders/mailboxes for an account.",
	}, s.listFolders)

	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "search_emails",
		Description: "Search emails in a folder. Returns summaries. Supports basic criteria.",
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
	Name         string `json:"name" jsonschema:"Human friendly name for the account"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port" jsonschema:"default:993"`
	IMAPUsername string `json:"imap_username"`
	IMAPPassword string `json:"imap_password" jsonschema:"The IMAP password (will be encrypted at rest)"`
	IMAPUseTLS   bool   `json:"imap_use_tls" jsonschema:"default:true"`

	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port" jsonschema:"default:587"`
	SMTPUsername string `json:"smtp_username"`
	SMTPPassword string `json:"smtp_password" jsonschema:"The SMTP password (will be encrypted at rest)"`
	SMTPUseTLS   bool   `json:"smtp_use_tls" jsonschema:"default:true"`

	FromAddress string `json:"from_address,omitempty"`
}

type AccountIDInput struct {
	AccountID string `json:"account_id" jsonschema:"The account ID"`
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
	Limit     int    `json:"limit,omitempty" jsonschema:"Max results, default 50"`
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
	if in.Name == "" || in.IMAPHost == "" || in.IMAPUsername == "" || in.SMTPHost == "" {
		return nil, nil, errors.New("required fields missing: name, imap_host, imap_username, smtp_host")
	}

	acc := &types.Account{
		Name:         in.Name,
		IMAPHost:     in.IMAPHost,
		IMAPPort:     defaultPort(in.IMAPPort, 993),
		IMAPUsername: in.IMAPUsername,
		IMAPUseTLS:   defaultBool(in.IMAPUseTLS, true),
		SMTPHost:     in.SMTPHost,
		SMTPPort:     defaultPort(in.SMTPPort, 587),
		SMTPUsername: in.SMTPUsername,
		SMTPUseTLS:   defaultBool(in.SMTPUseTLS, true),
		FromAddress:  in.FromAddress,
	}

	encIMAP, err := s.crypto.EncryptString(in.IMAPPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt imap password: %w", err)
	}
	acc.IMAPPasswordEnc = encIMAP

	encSMTP, err := s.crypto.EncryptString(in.SMTPPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt smtp password: %w", err)
	}
	acc.SMTPPasswordEnc = encSMTP

	if err := s.store.CreateAccount(ctx, acc); err != nil {
		return nil, nil, fmt.Errorf("store account: %w", err)
	}

	s.logger.Info("account added", "id", acc.ID, "name", acc.Name)

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Account created successfully. ID: %s", acc.ID)}},
	}, map[string]any{"id": acc.ID}, nil
}

func (s *Server) listEmailAccounts(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, map[string]any{"accounts": accounts}, nil
}

func (s *Server) removeEmailAccount(ctx context.Context, req *mcp.CallToolRequest, in AccountIDInput) (*mcp.CallToolResult, any, error) {
	if in.AccountID == "" {
		return nil, nil, errors.New("account_id required")
	}
	if err := s.store.DeleteAccount(ctx, in.AccountID); err != nil {
		return nil, nil, err
	}
	s.logger.Info("account removed", "id", in.AccountID)
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

	folder := in.Folder
	summaries, err := s.imapMgr.SearchEmails(ctx, acc, folder, crit, limit)
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

func (s *Server) getAccount(ctx context.Context, id string) (*types.Account, error) {
	if id == "" {
		return nil, errors.New("account_id is required")
	}
	acc, err := s.store.GetAccount(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("account not found: %s", id)
		}
		return nil, err
	}
	return acc, nil
}

func defaultPort(p, def int) int {
	if p > 0 {
		return p
	}
	return def
}

func defaultBool(b, def bool) bool {
	// The struct will have zero value false; we interpret explicit setting.
	// For simplicity, always use the value passed (user can set false).
	return b
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
