package types

import "time"

// Account represents a unified email account supporting both IMAP and SMTP.
type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// IMAP settings
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPUsername string `json:"imap_username"`
	// IMAPPassword is used when fetching from the Config API (plaintext).
	IMAPPassword string `json:"password,omitempty"`
	// IMAPPasswordEnc is stored encrypted in the database.
	IMAPPasswordEnc string `json:"-"`
	IMAPUseTLS      bool   `json:"imap_use_tls"`

	// SMTP settings
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	// SMTPPassword is used when fetching from the Config API (plaintext).
	SMTPPassword string `json:"smtp_password,omitempty"`
	// SMTPPasswordEnc is stored encrypted in the database.
	SMTPPasswordEnc string `json:"-"`
	SMTPUseTLS      bool   `json:"smtp_use_tls"`

	// Default sender address (optional). Falls back to SMTPUsername if empty.
	FromAddress string `json:"from_address,omitempty"`
}

// DecryptedCredentials holds plaintext credentials after decryption.
// Never persist or log these.
type DecryptedCredentials struct {
	IMAPPassword string
	SMTPPassword string
}

// AccountSummary is a safe view without any credential material.
type AccountSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	IMAPHost    string    `json:"imap_host"`
	IMAPPort    int       `json:"imap_port"`
	SMTPHost    string    `json:"smtp_host"`
	SMTPPort    int       `json:"smtp_port"`
	FromAddress string    `json:"from_address,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
