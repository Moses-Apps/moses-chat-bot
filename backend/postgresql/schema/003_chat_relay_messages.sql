CREATE TABLE chat_relay_messages (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id               UUID NOT NULL REFERENCES chat_relay_links(id) ON DELETE CASCADE,
    direction             VARCHAR(8) NOT NULL,
    provider_message_id   VARCHAR(255),
    moses_conversation_id UUID,
    text                  TEXT NOT NULL,
    metadata              JSONB DEFAULT '{}'::jsonb,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    error                 TEXT
);

CREATE UNIQUE INDEX idx_chat_relay_messages_provider_msg
    ON chat_relay_messages(provider_message_id, direction) WHERE provider_message_id IS NOT NULL;

CREATE INDEX idx_chat_relay_messages_link_time ON chat_relay_messages(link_id, occurred_at DESC);
