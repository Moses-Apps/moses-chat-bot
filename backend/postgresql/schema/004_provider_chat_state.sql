CREATE TABLE provider_chat_state (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id               UUID NOT NULL REFERENCES chat_relay_links(id) ON DELETE CASCADE,
    provider_chat_id      VARCHAR(255) NOT NULL,
    moses_conversation_id UUID,
    autopilot_enabled     BOOLEAN NOT NULL DEFAULT false,
    autopilot_session_id  UUID,
    settings              JSONB DEFAULT '{}'::jsonb,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(link_id, provider_chat_id)
);

CREATE INDEX idx_provider_chat_state_autopilot ON provider_chat_state(autopilot_session_id)
    WHERE autopilot_session_id IS NOT NULL;
