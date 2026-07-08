package types

import "time"

// Folder represents an IMAP mailbox/folder.
type Folder struct {
	Name        string   `json:"name"`
	Delimiter   string   `json:"delimiter,omitempty"`
	Attributes  []string `json:"attributes,omitempty"`
	NumMessages uint32   `json:"num_messages,omitempty"`
}

// EmailSummary provides header-level information for an email.
type EmailSummary struct {
	UID       uint32    `json:"uid"`
	SeqNum    uint32    `json:"seq_num,omitempty"`
	Subject   string    `json:"subject"`
	From      []Address `json:"from"`
	To        []Address `json:"to,omitempty"`
	Cc        []Address `json:"cc,omitempty"`
	Date      time.Time `json:"date"`
	Flags     []string  `json:"flags"`
	Size      uint32    `json:"size,omitempty"`
	HasAttach bool      `json:"has_attachments"`
}

// Address is an email address with optional display name.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// EmailMessage is a full email with body content.
type EmailMessage struct {
	EmailSummary
	Folder  string            `json:"folder"`
	Text    string            `json:"text,omitempty"`
	HTML    string            `json:"html,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Attachments lists metadata; content is fetched separately if needed.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Attachment metadata.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	PartID      string `json:"part_id,omitempty"` // for IMAP BODYSTRUCTURE reference if needed
}

// SendEmailInput for the send tool. Data for attachments must be base64.
type SendEmailInput struct {
	AccountID string   `json:"account_id"`
	To        []string `json:"to"`
	Cc        []string `json:"cc,omitempty"`
	Bcc       []string `json:"bcc,omitempty"`
	Subject   string   `json:"subject"`
	Text      string   `json:"text,omitempty"`
	HTML      string   `json:"html,omitempty"`
	From      string   `json:"from,omitempty"` // override default

	Attachments []AttachmentInput `json:"attachments,omitempty"`
}

// AttachmentInput for sending.
type AttachmentInput struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Data        string `json:"data"` // base64 encoded
}
