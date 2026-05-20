CREATE TABLE chat_relay_links (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    moses_user_id        UUID NOT NULL,
    tenant_id            UUID NOT NULL,
    provider             VARCHAR(32) NOT NULL,
    provider_user_id     VARCHAR(255) NOT NULL,
    encrypted_api_key    BYTEA NOT NULL,
    encryption_key_id    VARCHAR(64) NOT NULL,
    api_key_id_hint      UUID,
    is_active            BOOLEAN NOT NULL DEFAULT true,
    last_used_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deactivated_at       TIMESTAMPTZ,
    deactivation_reason  VARCHAR(64)
);

CREATE UNIQUE INDEX idx_chat_relay_links_active_provider_user
    ON chat_relay_links(provider, provider_user_id) WHERE is_active = true;

CREATE INDEX idx_chat_relay_links_moses_user ON chat_relay_links(moses_user_id, is_active);
CREATE INDEX idx_chat_relay_links_tenant ON chat_relay_links(tenant_id, is_active);
