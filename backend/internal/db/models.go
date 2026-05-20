package db

import (
	"time"

	"github.com/google/uuid"
)

type PendingLink struct {
	Code            string    `db:"code"`
	MosesUserID     uuid.UUID `db:"moses_user_id"`
	TenantID        uuid.UUID `db:"tenant_id"`
	EncryptedAPIKey []byte    `db:"encrypted_api_key"`
	EncryptionKeyID string    `db:"encryption_key_id"`
	APIKeyIDHint    *uuid.UUID `db:"api_key_id_hint"`
	ExpiresAt       time.Time `db:"expires_at"`
	CreatedAt       time.Time `db:"created_at"`
}

type ChatRelayLink struct {
	ID                 uuid.UUID  `db:"id"`
	MosesUserID        uuid.UUID  `db:"moses_user_id"`
	TenantID           uuid.UUID  `db:"tenant_id"`
	Provider           string     `db:"provider"`
	ProviderUserID     string     `db:"provider_user_id"`
	EncryptedAPIKey    []byte     `db:"encrypted_api_key"`
	EncryptionKeyID    string     `db:"encryption_key_id"`
	APIKeyIDHint       *uuid.UUID `db:"api_key_id_hint"`
	IsActive           bool       `db:"is_active"`
	LastUsedAt         *time.Time `db:"last_used_at"`
	CreatedAt          time.Time  `db:"created_at"`
	DeactivatedAt      *time.Time `db:"deactivated_at"`
	DeactivationReason *string    `db:"deactivation_reason"`
}

type ChatRelayMessage struct {
	ID                  uuid.UUID  `db:"id"`
	LinkID              uuid.UUID  `db:"link_id"`
	Direction           string     `db:"direction"`
	ProviderMessageID   *string    `db:"provider_message_id"`
	MosesConversationID *uuid.UUID `db:"moses_conversation_id"`
	Text                string     `db:"text"`
	Metadata            []byte     `db:"metadata"`
	OccurredAt          time.Time  `db:"occurred_at"`
	Error               *string    `db:"error"`
}

// TelegramBotConfig is the per-tenant Telegram bot connection (one row per
// tenant). EncryptedToken holds the BotFather token sealed under the per-tenant
// crypto envelope; EncryptionKeyID names the master-key version used.
type TelegramBotConfig struct {
	TenantID          uuid.UUID `db:"tenant_id"`
	EncryptedToken    []byte    `db:"encrypted_token"`
	EncryptionKeyID   string    `db:"encryption_key_id"`
	BotID             *int64    `db:"bot_id"`
	BotUsername       *string   `db:"bot_username"`
	WebhookSecret     string    `db:"webhook_secret"`
	ConnectedByUserID uuid.UUID `db:"connected_by_user_id"`
	ConnectedAt       time.Time `db:"connected_at"`
	UpdatedAt         time.Time `db:"updated_at"`
}

type ProviderChatState struct {
	ID                  uuid.UUID  `db:"id"`
	LinkID              uuid.UUID  `db:"link_id"`
	ProviderChatID      string     `db:"provider_chat_id"`
	MosesConversationID *uuid.UUID `db:"moses_conversation_id"`
	AutopilotEnabled    bool       `db:"autopilot_enabled"`
	AutopilotSessionID  *uuid.UUID `db:"autopilot_session_id"`
	Settings            []byte     `db:"settings"`
	CreatedAt           time.Time  `db:"created_at"`
	UpdatedAt           time.Time  `db:"updated_at"`
}
