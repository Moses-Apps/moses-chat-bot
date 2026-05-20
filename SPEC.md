# moses-chat-bot — Specification

> **Status**: Spec v1 (pending review). Last updated 2026-05-19.
> **Scope**: Self-contained Moses app that relays Moses-Manager chats between mobile chat providers (Telegram first; Discord/Slack later) and a user's authenticated Moses session, plus exposes an OpenAPI surface so Moses Manager can push completion / status messages back to the user's chat.

---

## 1. Goal

Make Moses usable from mobile by bridging the platform's authenticated Moses-Manager chat to any consumer chat provider. A user sends a Telegram message → it relays to their Moses Manager (full MM tool surface, full conversation continuity) → the response streams back as Telegram messages. Moses Manager can also push messages out to the same Telegram chat ("deploy succeeded", "ticket DONE", "approval needed") via the bot's workspace-tool API.

## 2. Non-goals (v1)

- Voice synthesis of responses (text out only; voice in via Whisper is optional v1).
- Group chats / multi-user channels (1:1 per Moses user only).
- Cross-tenant linking (one Telegram account ↔ one (Moses user, tenant) pair).
- Web UI for the chat itself (Moses Manager web UI already serves that purpose; this app's frontend is administrative — link/unlink, manage settings, see relay history).

## 3. Architecture overview

> **Note**: `POST /api/v1/chat/conversations/:id/messages` is a **persistence-only** handler (`backend/internal/api/chat_handlers.go:265`). It does NOT run Moses Manager. MM execution lives at `POST /api/v1/ai/chat/stream` (`ai_chat_handlers.go:367`), which writes the assistant turn to the conversation as a side-effect and emits streaming events on the `/api/v1/ai/ws?token=mcp-...` WebSocket. The bot uses the streaming endpoint + WS subscription so multi-tool agentic loops (minutes long) don't time out a sync HTTP call.

```
Telegram user (phone)
   │
   │ Telegram messenger
   ▼
Telegram Bot API (api.telegram.org)
   │
   │ webhook POST (Telegram secret-token header verified)
   ▼
moses-chat-bot backend (this app, in-cluster)
   │ 1. Resolve (telegram_user_id → active chat_relay_link → user's encrypted API key)
   │ 2. Resolve (telegram_chat_id → existing moses_conversation_id OR create one via
   │    POST /api/v1/chat/conversations using the user's key — cookie/key-bearer auth)
   │ 3. Open a WS to /api/v1/ai/ws?token=mcp-<user_key>, subscribe to the resolved
   │    conversationID (or rely on the user-scoped fan-out the AI hub already does)
   │ 4. POST /api/v1/ai/chat/stream with the user's API key
   │    Body: {message, conversationId, modelOverride?: omitted}
   ▼
moses-backend (cluster service)
   │ - AuthMiddlewareWithAPIKey resolves Bearer/X-API-Key → user, tenant, profile
   │ - RBACMiddleware checks USE AI (profile=moses-manager-full carries it)
   │ - AIHandler.StreamChatMessage dispatches to Claude Code / OpenCode / Codex / Gemini
   │   session pod per user_ai_configurations cascade
   │ - Session pod runs Moses Manager with the full ~84-tool surface, streams events
   │   over the WebSocket back to subscribed clients (the bot is one)
   ▼
moses-chat-bot backend
   │ - Aggregates streaming `assistant_message_chunk` events into a complete turn
   │ - On `assistant_message_complete`, sends the final text back to Telegram Bot API
   │   as one or more messages (chunked at 4096 chars on word boundaries)
   │ - Persists turn pair in chat_relay_messages table
```

The sync `POST /api/v1/ai/chat` endpoint is the fallback when the bot can't hold a WS open (degraded mode). Default path is streaming.

Push direction (MM → user):

```
Moses Manager (inside Moses)
   │ wants to message a user (deploy succeeded, etc.)
   ▼
Workspace-tool call: moses-chat-bot exposes POST /api/v1/push/messages
   │ (auto-discovered as workspace_moses-chat-bot_pushMessage)
   ▼
moses-chat-bot backend
   │ 1. Resolve (moses_user_id → linked provider chats)
   │ 2. For each linked chat, send message via provider adapter
   ▼
Telegram Bot API → user's phone
```

## 4. Auth model — Type C (per-user MCP API keys)

The user-facing flow:

1. User installs moses-chat-bot from marketplace (admin one-time).
2. User opens bot's frontend (embedded in Moses) and clicks **Link Telegram**.
3. The frontend (running inside the iframe under `/apps/<tenant>/moses-chat-bot/`, same-origin with the Moses session cookie) calls `POST /api/v1/api-keys` directly with:
   ```json
   { "name": "telegram:bot:<placeholder>", "profile": "moses-manager-full",
     "expiresAt": "<+90d>" }
   ```
   `credentials: 'include'` carries the `access_token` cookie. Returns the plaintext key once.
   (This requires PLATFORM-1: the user's session must be authorized to request `moses-manager-full`. The handler enforces it.)
4. The frontend forwards the plaintext key + a fresh code-mint to the bot backend: `POST /api/v1/links/codes` with `{ apiKey, expiresInSeconds: 60 }`. Bot stores `(code, moses_user_id, tenant_id, encrypted_api_key, expires_at)` in `pending_links` (60s TTL). The plaintext key never re-enters the frontend after this hop.
5. UI displays the 6-digit code huge with countdown timer.
6. User opens Telegram, talks to `@moses_yourtenant_bot`, sends `/link 123456`.
7. Bot validates the code (constant-time compare). Rate limit: per `provider_user_id`, ≥3 failures = 15 min lockout. Requires the user to have sent `/start` once prior.
8. Bot resolves `(moses_user_id, tenant_id, encrypted_api_key)` from `pending_links`, copies to `chat_relay_links` row, deletes `pending_links` row.
9. Bot replies in Telegram: "Linked successfully. Talk to me anytime."

**Why the frontend mints the key directly rather than the bot-backend doing it on the user's behalf**: the iframe-SDK + `mosesproxy` round-trip is **platform-actions-only** (`platform_action_dispatcher.go` only knows `chat_prompt` and `launch_agent`). Direct `/api/v1/api-keys` cannot travel through that proxy. The frontend's same-origin cookie auth is the cleanest path.

**Key revocation cycle**: user can revoke from Moses UI → bot detects 401 on next moses-backend call → marks link inactive with `deactivation_reason = "platform_401"` → tells Telegram user to re-link. The bot also stores the returned `api_key_id` in `api_key_id_hint` so it can call `DELETE /api/v1/api-keys/:id` on `/unlink` (best-effort).

The minting in step 6 requires **platform GAP-1 fix** (see §8): `CreateUserAPIKeyHandler` must accept `profile` with RBAC gating.

From here on:
- Every Telegram message → bot decrypts the key → forwards as `Authorization: Bearer <key>` to moses-backend.
- The key carries the user's permissions natively. No `X-Moses-User-ID` impersonation.
- User can revoke from Moses UI → bot detects 401 on next use → marks link inactive → tells Telegram user to re-link.

## 5. Provider-adapter abstraction

Generic interface, plug-in pattern. Add Discord/Slack later as ~one new package each.

```go
// internal/provider/provider.go
type Provider interface {
    Name() string                                   // "telegram"
    HandleWebhook(ctx context.Context, body []byte, headers http.Header) ([]InboundMessage, error)
    SendMessage(ctx context.Context, chat ChatRef, msg OutboundMessage) error
    SetupWebhook(ctx context.Context, baseURL string) error
    VerifyWebhookSignature(headers http.Header, body []byte) error
}

type InboundMessage struct {
    Provider       string    // "telegram"
    ProviderUserID string    // telegram user_id as string
    ProviderChatID string    // telegram chat_id as string (1:1 chat = same as user)
    Text           string
    Attachments    []Attachment   // photos, voice notes
    ReceivedAt     time.Time
    RawJSON        []byte         // for audit
}

type OutboundMessage struct {
    Text        string
    Markdown    bool         // platform-specific rendering hint
    ReplyToID   string       // for threaded replies where supported
}

type ChatRef struct {
    Provider       string
    ProviderChatID string
}
```

Provider registry resolved at startup via `internal/provider/registry.go`. v1 ships Telegram only.

## 6. Conversation storage

Three tables (provisioned via app-data git OR app-bundled PostgreSQL via `dependencies.services: ["postgresql"]`; using PostgreSQL for v1 because the relay rate and indexing requirements rule out git-as-DB):

```sql
-- 001_pending_links.sql
CREATE TABLE pending_links (
    code            VARCHAR(6) PRIMARY KEY,
    moses_user_id   UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_pending_links_expires ON pending_links(expires_at);

-- 002_chat_relay_links.sql
CREATE TABLE chat_relay_links (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    moses_user_id        UUID NOT NULL,
    tenant_id            UUID NOT NULL,
    provider             VARCHAR(32) NOT NULL,           -- "telegram"
    provider_user_id     VARCHAR(255) NOT NULL,
    encrypted_api_key    BYTEA NOT NULL,                 -- AES-256-GCM ciphertext
    encryption_key_id    VARCHAR(64) NOT NULL,           -- which DEK was used
    api_key_id_hint      UUID,                           -- moses-backend mcp_api_keys.id (for revoke detection)
    is_active            BOOLEAN NOT NULL DEFAULT true,
    last_used_at         TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deactivated_at       TIMESTAMPTZ,
    deactivation_reason  VARCHAR(64),                    -- "user_unlink" | "key_revoked" | "platform_401"
    -- partial uniqueness must be a separate INDEX in Postgres
);
CREATE UNIQUE INDEX idx_chat_relay_links_active_provider_user
    ON chat_relay_links(provider, provider_user_id) WHERE is_active = true;
CREATE INDEX idx_chat_relay_links_moses_user ON chat_relay_links(moses_user_id, is_active);

-- 003_chat_relay_messages.sql
CREATE TABLE chat_relay_messages (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id             UUID NOT NULL REFERENCES chat_relay_links(id) ON DELETE CASCADE,
    direction           VARCHAR(8) NOT NULL,             -- "in" | "out"
    provider_message_id VARCHAR(255),                    -- for dedup of inbound + correlation
    moses_conversation_id UUID,                          -- chat_conversations.id on the platform side
    text                TEXT NOT NULL,
    metadata            JSONB DEFAULT '{}'::jsonb,       -- attachments, model used, latency, etc.
    occurred_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    error               TEXT                             -- non-null = delivery failed
    -- partial uniqueness moved to a separate INDEX (Postgres syntax requirement)
);
CREATE UNIQUE INDEX idx_chat_relay_messages_provider_msg
    ON chat_relay_messages(provider_message_id, direction) WHERE provider_message_id IS NOT NULL;
CREATE INDEX idx_chat_relay_messages_link_time ON chat_relay_messages(link_id, occurred_at DESC);

-- 004_provider_chat_state.sql
-- One row per (link, provider_chat_id) — for 1:1 Telegram this equals link, but Discord/Slack
-- have N chats per linked user, so the conversation ID is per-chat, not per-link.
CREATE TABLE provider_chat_state (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    link_id                  UUID NOT NULL REFERENCES chat_relay_links(id) ON DELETE CASCADE,
    provider_chat_id         VARCHAR(255) NOT NULL,
    moses_conversation_id    UUID,                       -- reused across turns
    autopilot_enabled        BOOLEAN NOT NULL DEFAULT false,
    autopilot_session_id     UUID,                       -- active autonomous_sessions row
    settings                 JSONB DEFAULT '{}'::jsonb,  -- per-chat prefs: notifications, voice_in_enabled, etc.
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(link_id, provider_chat_id)
);
```

**Encryption**:
- Master key Secret format: JSON object `{"keys": {"v1": "<base64-32-bytes>", "v2": ...}, "active": "v1"}`. Loaded once at startup; hot-reloaded on SIGHUP.
- Per-tenant DEK derivation: `dek = HKDF-SHA256(master_keys[v], salt = "moses-chat-bot.v1" || sha256(tenant_id), info = "api-key/v1", L = 32)`. Salt is a constant string prefix + tenant-id digest so two tenants never collide and deployments never silently re-derive under a stale identifier.
- Each row stores `encryption_key_id` = the master-version label (`"v1"`).
- **Rotation**: ops adds `v2` to the master Secret + flips `active` → `v2`. New ciphertexts written under v2. A background sweeper re-encrypts rows whose `encryption_key_id` ≠ active over time; rotation is non-disruptive. A row whose `encryption_key_id` references a master version not in the Secret is **marked inactive** with `deactivation_reason = "master_key_unavailable"` — the user is asked to re-link rather than being silently broken.
- **Master loss recovery**: zero data loss is impossible if the master is lost; users re-link. The K8s Secret should be backed up via existing tenant-level GitOps + restored from there.

## 7. Workspace-tool API surface (MM → user push)

The bot serves OpenAPI at `/api/openapi.json` so Moses auto-discovers these as MCP tools:

| Method | Path | Operation ID | Purpose |
|--------|------|--------------|---------|
| POST | `/api/v1/push/message` | `pushMessage` | Send a message to a specific user's linked chats. Body: `{ moses_user_id, text, markdown?, provider_filter?: ["telegram"], chat_filter?: [chat_id] }`. |
| GET | `/api/v1/links` | `listLinks` | List active links for the calling tenant. MM can ask "who has Telegram linked?". |
| POST | `/api/v1/links/:id/notify` | `notifyLink` | Send to a single link by ID. Cheaper than `pushMessage` if MM already knows the link. |
| GET | `/api/v1/messages` | `listRecentMessages` | Pull recent relay history for a user (audit / context for MM). Limited to last 100. |

Moses platform's `SanitizeToolName` (`backend/internal/services/openapi_parser.go:417`) replaces non-alphanumeric with underscores in the tool key. The app slug `moses-chat-bot` therefore becomes the MCP-tool prefix `moses_chat_bot`. The four endpoints surface as:

- `workspace_moses_chat_bot_pushMessage`
- `workspace_moses_chat_bot_listLinks`
- `workspace_moses_chat_bot_notifyLink`
- `workspace_moses_chat_bot_listRecentMessages`

Caller authorization: the workspace-tool wedge already authenticates via `MOSES_PLATFORM_API_KEY` mounted into MM's containers. We additionally enforce that the calling tenant matches the link's tenant before sending — defense in depth.

### Caller authentication (CHAT-y3u follow-up)

The workspace-tool surface (`/api/v1/push/*`, `/api/v1/workspace/*`) is externally reachable via the ingress (`helm/templates/ingress.yaml` routes `/api/` to the backend service). The bot therefore enforces a bearer-token gate as the first middleware on that surface:

- **Middleware**: `backend/internal/handler/middleware/platform_auth.go::RequirePlatformAPIKey`
- **Expected token**: the bot reads `MOSES_PLATFORM_API_KEY` from the env (mounted by Moses when the `moses-platform` integration grant is approved — see `moses-app.config.json` `integrations.required[0]`).
- **Compare**: `crypto/subtle.ConstantTimeCompare` against the inbound `Authorization: Bearer <token>` header.
- **Fail-closed**: if `MOSES_PLATFORM_API_KEY` is unset, every request is rejected with `503 platform_key_unset`. The gate refuses to serve until the integration is wired up.
- **Local-dev escape hatch**: setting `BOT_PLATFORM_AUTH_DISABLED=true` bypasses the bearer check (the middleware logs a warn on every request when set). Never use this outside a developer laptop.

Only after the bearer gate passes does the `MosesHeaders` middleware extract `X-Moses-Tenant-ID` and stamp it into context — i.e. the tenant header is trustworthy only because we've already proved the caller is the platform proxy. Cross-tenant calls still surface a 403 from the handler-layer tenant binding check (`handlePushMessage` / `handleNotifyLink`), keeping the defense-in-depth note above intact.

## 8. Platform gap-close (PR against moses-platform-prep)

**Single gap identified during verification**: `backend/internal/api/middleware_apikey.go:329 CreateUserAPIKeyHandler` does not accept a `profile` field. User-minted keys end up with empty profile, which means `allowedTools` is empty (line 149 of middleware_apikey.go: profile-based fallback only fires when `mcpKey.Profile != ""`) — the key is effectively unusable.

**Fix scope** (narrow, per user decision):

1. Add `Profile string` to the request body of `CreateUserAPIKeyHandler`.
2. Whitelist of allowed profiles for user self-mint: `external-minimal`, `moses-manager-full`. (Autonomous and agent profiles remain admin-only — tracked but out of scope.)
3. RBAC gating per profile:
   - `external-minimal`: existing `CREATE MCP_API_KEYS` permission.
   - `moses-manager-full`: requires `USE AI` permission AND `CREATE MCP_API_KEYS`. (Already the gate for using MM in-browser.)
4. Default profile when omitted: `external-minimal` (preserves current behavior).
5. Audit log: log the chosen profile on key creation.
6. Add a test covering each allowed profile + a rejected one.

That's all. Single-file change + test. Estimated ~80 LOC.

## 9. Autopilot enablement from the bot

The user can type `/autopilot start` in Telegram. The bot:

1. **Pre-flight check**: calls `GET /api/v1/autonomous/active`. The autonomous session is **tenant-singleton** (`autonomous_handlers.go:91`, `autonomous_session_store.GetActiveAutonomousSession` returns per-tenant; `StartSession` would auto-cancel any existing). If a session exists AND its owner is a different user, the bot **refuses** with: "Tenant already has an active autopilot session owned by another user; ask them to stop it first." If owned by the same user, return the existing session info.
2. If clear, calls `POST /api/v1/autonomous/start` with the user's API key. Body uses platform defaults (3 concurrent, 24h timeout). RBAC: the user must already have `CREATE AUTONOMOUS_SESSIONS` permission — bot doesn't grant it.
3. Stores the returned `autonomous_session_id` in `provider_chat_state.autopilot_session_id`.
4. Reports back: "Autopilot started. Session abc12345 — say /autopilot stop to halt."
5. `/autopilot stop` → `POST /api/v1/autonomous/:id/stop`.
6. `/autopilot status` → `GET /api/v1/autonomous/:id` formatted as a Telegram message.

**Background cleanup**: a periodic sweeper (every 60s) polls `GET /api/v1/autonomous/:id` for any non-null `provider_chat_state.autopilot_session_id`. If the platform reports the session in a terminal state (`completed | cancelled | failed`), the sweeper clears the column and DMs the user a one-line summary (the existing completion-aggregator stamp from CHAT-e2zp/sx1y/p4cy is the source — bot just relays it).

When MM runs in autopilot it executes against the user's full MM-autonomous tool surface (~92 tools). The bot does not need anything special for this — the user's API key, if minted with `moses-manager-full` profile, gets the same downstream MM behavior. The autopilot session is owned by the user, not the bot.

**Note**: minting `moses-manager-autonomous`-profile keys is admin-only (out of scope per §8). A user wanting autopilot from Telegram uses their `moses-manager-full` key — autopilot is enabled at session-level via the `/autonomous/start` endpoint, not at API-key-profile level. This is the existing model.

## 10. Mobile-ready frontend (admin / linking UI)

Embedded in Moses via iframe (subpath `/apps/<tenant>/moses-chat-bot/`). Responsive (works in Moses desktop AND in Tauri installer's webview). Pages:

| Route | Purpose |
|-------|---------|
| `/` | Dashboard: list of my active links, last-used timestamps, "Link new chat" button. |
| `/link/new` | Pick provider → generate 6-digit code → poll for completion → show success. |
| `/links/:id` | Detail: chat history (last 100 turns), settings toggles, **Unlink** button. |
| `/messages` | Relay history across all my links with search + filter. |
| `/settings` | Per-user: default notification preferences, do-not-disturb hours, autopilot defaults. |

Design language: matches Moses' Bento Grid + 4px spacing. Uses Tailwind for fast styling. Layout works at 320px (small phones embedded in Tauri). Voice input toggle on `/settings`.

Stack: React 19 + TypeScript + Vite + Tailwind. State via Zustand. iframe-SDK auto-loaded for in-Moses session continuity.

## 11. Telegram adapter specifics

- Bot creation: handled by tenant admin via BotFather, token saved as K8s secret `telegram-bot-token` mounted via standard `secrets.secretNames[]` and read by config loader. One bot per tenant.
- Webhook URL: `https://<moses-host>/apps/<tenant>/moses-chat-bot/api/v1/providers/telegram/webhook`. The bot's `/setup` endpoint registers this with Telegram on deploy via `provider.SetupWebhook`.
- Signature verification: Telegram doesn't sign webhooks; instead we configure a secret token (`secret_token` field on `setWebhook`) which Telegram sends as `X-Telegram-Bot-Api-Secret-Token` header. Bot validates per request.
- Long messages: Telegram caps at 4096 chars. Bot chunks responses with sensible word-boundary splitting.
- Voice in: deferred to v1.1. The platform does not currently expose a transcription tool the bot could call; v1.1 ships a Whisper sidecar container declared via `dependencies.services` and invoked directly by the bot backend. v1 rejects voice notes politely.
- Photo in: forwards photo URL to MM as a message attachment; MM handles via its multimodal path.

## 12. Slash commands (Telegram)

| Command | Action |
|---------|--------|
| `/start` | Welcome message + linking instructions. |
| `/link <code>` | Complete linking using code minted in web UI. |
| `/unlink` | Soft-unlink: marks link inactive but preserves history. |
| `/help` | List commands. |
| `/use <tenant_slug>` | For multi-tenant users, switch active tenant. |
| `/tickets` | Show user's open tickets (calls MM with structured prompt). |
| `/status` | Show platform status / autopilot session if any. |
| `/autopilot start\|stop\|status` | See §9. |
| `/clear` | Start a fresh Moses conversation (new chat_conversation row). |
| `/dnd <duration>` | Snooze outbound pushes for N hours. |

Non-command messages → forwarded as regular chat to MM.

## 13. moses-app.config.json

```jsonc
{
  "name": "moses-chat-bot",
  "version": "0.1.0",
  "displayName": "Chat Bot Bridge",
  "description": "Bridge Moses Manager to mobile chat (Telegram, Discord, Slack)...",
  "appType": "hybrid",
  "entrypoint": "index.html",
  "templateApiVersion": "moses.ai/v1",
  "embedding": { "framing": "moses-only" },
  "displayLocations": ["apps"],
  "permissions": [],
  "skills": ["skills/bot-architecture.md"],
  "apiEndpoints": {
    "basePath": "/api/v1",
    "healthPath": "/health",
    "specPath": "/api/openapi.json"
  },
  "autoRegisterTool": true,
  "appData": { "enabled": false },  // We use postgres; app-data git is overkill.
  "helm": { "chartPath": "helm" },
  "docker": {
    "files": [
      { "name": "frontend", "dockerfile": "frontend/Dockerfile", "context": "frontend" },
      { "name": "backend",  "dockerfile": "backend/Dockerfile",  "context": "backend"  }
    ]
  },
  "services": {
    "frontend": {
      "port": 8080,
      "healthCheck": { "enabled": true, "path": "/health" },
      "ingress": { "enabled": true },
      "resources": { "cpu": { "request": "50m", "limit": "200m" }, "memory": { "request": "64Mi", "limit": "256Mi" } }
    },
    "backend": {
      "port": 8080,
      "healthCheck": { "enabled": true, "path": "/health" },
      "resources": { "cpu": { "request": "100m", "limit": "500m" }, "memory": { "request": "128Mi", "limit": "512Mi" } }
    }
  },
  "dependencies": { "services": ["postgresql"] },
  "integrations": {
    "required": [
      {
        "type": "moses-platform",
        "key": "default",
        "scopes": ["chat_relay", "workspace_push"],
        "envMapping": { "apiKey": "MOSES_PLATFORM_API_KEY", "apiUrl": "MOSES_PLATFORM_API_URL" }
      }
    ]
  },
  "validation": {
    "enabled": true,
    "commands": [
      { "name": "backend-vet",  "run": "cd backend && go vet ./...",  "required": true },
      { "name": "backend-test", "run": "cd backend && go test ./...", "required": true },
      { "name": "frontend-lint","run": "cd frontend && npm install --include=dev --ignore-scripts && npm run lint", "required": true },
      { "name": "frontend-test","run": "cd frontend && npm test -- --run", "required": true }
    ]
  }
}
```

## 14. Test plan

- **Unit**: provider adapter contract tests, encryption round-trip, slash-command parser, message chunking, dedup logic.
- **Integration (backend)**: stub Telegram + stub moses-backend → drive full inbound/outbound flow, assert DB state and outbound calls.
- **Integration (with platform)**: spin up moses-backend in a test container, mint a real user key, drive the bot, assert chat conversations land in `chat_conversations` and are reused across turns.
- **Frontend**: component tests for linking flow, accessibility (axe), responsive snapshots at 320/768/1280.
- **E2E**: a Playwright run that links a fake Telegram user → sends "hello" → asserts MM-style response → asserts row in `chat_relay_messages`.

## 15. Open architecture decisions resolved

- **DB**: PostgreSQL via `dependencies.services` (NOT app-data git — relay rate + indexing rule out git).
- **First provider**: Telegram only; adapter pattern proven by Telegram being real, others as beads.
- **Push direction**: workspace-tool OpenAPI surface (MM-discoverable), not webhook trigger rules. Trigger rules can additionally fan out by pointing their `webhook` action at our push endpoint — but that's an app-config concern, not a bot capability.
- **Platform PR**: narrow profile-gate PR only.

## 16. Release plan

- v0.1.0: Telegram inbound + outbound MM relay + linking + push API + autopilot start/stop/status + slash commands listed above + frontend admin shell. Marketplace-published.
- v0.2.0: Voice-in via Whisper sidecar; multi-message-context (multi-turn history accumulation per chat).
- v0.3.0: Discord adapter.
- v0.4.0: Slack adapter.
