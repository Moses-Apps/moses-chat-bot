// Package telegram contains the Telegram Bot API adapter implementing
// provider.Provider. Only the fields actually consumed by the relay are
// decoded; new fields can be added incrementally without versioning.
package telegram

// Update mirrors a Telegram Bot API Update object.
// See https://core.telegram.org/bots/api#update.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message,omitempty"`
}

// Message is the subset of fields the relay consumes from an inbound message.
type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from,omitempty"`
	Chat      Chat        `json:"chat"`
	Date      int64       `json:"date"`
	Text      string      `json:"text,omitempty"`
	Caption   string      `json:"caption,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
	Voice     *Voice      `json:"voice,omitempty"`
	Document  *Document   `json:"document,omitempty"`
}

// User is the subset of fields the relay consumes about the sender.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat is the subset of fields the relay consumes about the chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type,omitempty"`
}

// PhotoSize is one of the available resolutions of a photo.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Voice describes a voice-note attachment.
type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// Document describes a generic file attachment.
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int    `json:"file_size,omitempty"`
}

// apiResponse is the envelope every Bot API method returns.
type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Parameters  *respParameters `json:"parameters,omitempty"`
	// Result is intentionally raw — callers decode if they care.
	Result []byte `json:"-"`
}

// respParameters carries optional rate-limit hints.
type respParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

// WebhookInfo is a subset of the getWebhookInfo response.
type WebhookInfo struct {
	URL                  string `json:"url"`
	HasCustomCertificate bool   `json:"has_custom_certificate"`
	PendingUpdateCount   int    `json:"pending_update_count"`
}
