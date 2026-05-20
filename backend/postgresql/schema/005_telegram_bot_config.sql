-- Per-tenant Telegram bot configuration (moses-chat-bot-qcq).
--
-- One row per tenant. The bot token a tenant admin obtains from @BotFather is
-- stored encrypted at rest under the per-tenant crypto envelope (see
-- backend/internal/service/crypto) — never plaintext. encryption_key_id records
-- which master-key version sealed encrypted_token so it can be decrypted after
-- a key rotation.
--
-- webhook_secret is the value passed to Telegram's setWebhook secret_token; the
-- inbound webhook handler verifies the X-Telegram-Bot-Api-Secret-Token header
-- against it. connected_by_user_id is the tenant admin who ran the in-app
-- Connect wizard (audit trail).
CREATE TABLE telegram_bot_config (
    tenant_id            UUID PRIMARY KEY,
    encrypted_token      BYTEA NOT NULL,
    encryption_key_id    VARCHAR(64) NOT NULL,
    bot_id               BIGINT,
    bot_username         VARCHAR(255),
    webhook_secret       VARCHAR(128) NOT NULL,
    connected_by_user_id UUID NOT NULL,
    connected_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
