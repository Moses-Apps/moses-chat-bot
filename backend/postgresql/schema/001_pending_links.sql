CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE pending_links (
    code              VARCHAR(6) PRIMARY KEY,
    moses_user_id     UUID NOT NULL,
    tenant_id         UUID NOT NULL,
    encrypted_api_key BYTEA NOT NULL,
    encryption_key_id VARCHAR(64) NOT NULL,
    api_key_id_hint   UUID,
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_pending_links_expires ON pending_links(expires_at);
CREATE INDEX idx_pending_links_tenant_user ON pending_links(tenant_id, moses_user_id);
