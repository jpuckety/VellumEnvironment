package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/google/uuid"
	"github.com/jpuckett/EmailMCP/emailmcp/internal/types"
)

var (
	ErrNotFound = errors.New("account not found")
)

// Store manages persistent account data.
type Store struct {
	db *sql.DB
}

// New opens (and initializes) the SQLite database.
func New(ctx context.Context, dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite is better with limited writers
	db.SetMaxIdleConns(1)

	s := &Store{db: db}

	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS accounts (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,

		imap_host TEXT NOT NULL,
		imap_port INTEGER NOT NULL,
		imap_username TEXT NOT NULL,
		imap_password_enc TEXT NOT NULL,
		imap_use_tls INTEGER NOT NULL DEFAULT 1,

		smtp_host TEXT NOT NULL,
		smtp_port INTEGER NOT NULL,
		smtp_username TEXT NOT NULL,
		smtp_password_enc TEXT NOT NULL,
		smtp_use_tls INTEGER NOT NULL DEFAULT 1,

		from_address TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_accounts_name ON accounts(name);
	`

	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}

// CreateAccount inserts a new account. ID is generated if empty.
func (s *Store) CreateAccount(ctx context.Context, acc *types.Account) error {
	if acc.ID == "" {
		acc.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	acc.CreatedAt = now
	acc.UpdatedAt = now

	query := `
		INSERT INTO accounts (
			id, name, created_at, updated_at,
			imap_host, imap_port, imap_username, imap_password_enc, imap_use_tls,
			smtp_host, smtp_port, smtp_username, smtp_password_enc, smtp_use_tls,
			from_address
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := s.db.ExecContext(ctx, query,
		acc.ID, acc.Name, acc.CreatedAt, acc.UpdatedAt,
		acc.IMAPHost, acc.IMAPPort, acc.IMAPUsername, acc.IMAPPasswordEnc, boolToInt(acc.IMAPUseTLS),
		acc.SMTPHost, acc.SMTPPort, acc.SMTPUsername, acc.SMTPPasswordEnc, boolToInt(acc.SMTPUseTLS),
		acc.FromAddress,
	)
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}

// GetAccount retrieves full account (including encrypted fields).
func (s *Store) GetAccount(ctx context.Context, id string) (*types.Account, error) {
	query := `
		SELECT id, name, created_at, updated_at,
		       imap_host, imap_port, imap_username, imap_password_enc, imap_use_tls,
		       smtp_host, smtp_port, smtp_username, smtp_password_enc, smtp_use_tls,
		       from_address
		FROM accounts WHERE id = ?
	`

	row := s.db.QueryRowContext(ctx, query, id)

	var acc types.Account
	var imapTLS, smtpTLS int

	err := row.Scan(
		&acc.ID, &acc.Name, &acc.CreatedAt, &acc.UpdatedAt,
		&acc.IMAPHost, &acc.IMAPPort, &acc.IMAPUsername, &acc.IMAPPasswordEnc, &imapTLS,
		&acc.SMTPHost, &acc.SMTPPort, &acc.SMTPUsername, &acc.SMTPPasswordEnc, &smtpTLS,
		&acc.FromAddress,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get account: %w", err)
	}

	acc.IMAPUseTLS = imapTLS != 0
	acc.SMTPUseTLS = smtpTLS != 0
	return &acc, nil
}

// ListAccounts returns summaries (no credential material).
func (s *Store) ListAccounts(ctx context.Context) ([]types.AccountSummary, error) {
	query := `
		SELECT id, name, created_at, updated_at,
		       imap_host, imap_port, smtp_host, smtp_port, from_address
		FROM accounts ORDER BY name, created_at
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var out []types.AccountSummary
	for rows.Next() {
		var s types.AccountSummary
		if err := rows.Scan(
			&s.ID, &s.Name, &s.CreatedAt, &s.UpdatedAt,
			&s.IMAPHost, &s.IMAPPort,
			&s.SMTPHost, &s.SMTPPort, &s.FromAddress,
		); err != nil {
			return nil, fmt.Errorf("scan account summary: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteAccount removes an account.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAccount allows partial updates. Only non-zero / non-empty fields are updated.
func (s *Store) UpdateAccount(ctx context.Context, id string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}

	// Build dynamic update. Only whitelisted fields.
	allowed := map[string]bool{
		"name":      true,
		"imap_host": true, "imap_port": true, "imap_username": true, "imap_password_enc": true, "imap_use_tls": true,
		"smtp_host": true, "smtp_port": true, "smtp_username": true, "smtp_password_enc": true, "smtp_use_tls": true,
		"from_address": true,
	}

	setClauses := []string{}
	args := []any{}
	for k, v := range updates {
		if !allowed[k] {
			continue
		}
		setClauses = append(setClauses, k+" = ?")
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return nil
	}

	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, time.Now().UTC())
	args = append(args, id)

	query := fmt.Sprintf("UPDATE accounts SET %s WHERE id = ?", joinClauses(setClauses))

	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func joinClauses(clauses []string) string {
	result := ""
	for i, c := range clauses {
		if i > 0 {
			result += ", "
		}
		result += c
	}
	return result
}
