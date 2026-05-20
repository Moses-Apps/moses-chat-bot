# Privacy

What moses-chat-bot stores about you, where, and for how long. Version 0.1.0.

## What the bot stores

| Data | Where | Encryption | Retention |
|------|-------|------------|-----------|
| Your Moses MCP API key | `chat_relay_links.encrypted_api_key` (Postgres). | AES-256-GCM, DEK derived per-tenant via HKDF-SHA256 from a master key in a K8s Secret. | Until you unlink (then soft-marked inactive but ciphertext retained for audit; hard-delete is a v1.1 follow-up). |
| Your Telegram user ID (paired with your Moses user ID) | `chat_relay_links.provider_user_id`. | Plaintext (it's not secret on Telegram's side). | Same as above. |
| Message text you sent and received via the bot | `chat_relay_messages.text`. | Plaintext at rest (inside the bot's Postgres). | **No automatic cleanup in v0.1.0.** Cleanup is a v1.1 follow-up; today, history accumulates indefinitely. |
| Telegram message IDs (for dedup and correlation) | `chat_relay_messages.provider_message_id`. | Plaintext. | Same as message text. |
| Moses conversation IDs (so MM keeps context across turns per chat) | `provider_chat_state.moses_conversation_id`. | Plaintext. | Same as link. |
| Per-chat settings (notification toggles, autopilot defaults) | Today: `localStorage` in your browser. Cross-device sync is on the roadmap. | n/a (your browser). | Until you clear your browser storage. |
| Audit metadata: timestamps, attachment file IDs, error strings | `chat_relay_messages.metadata` (JSONB), `*.occurred_at`, `chat_relay_links.last_used_at`. | Plaintext. | Same as message text. |

## What the bot does **not** store

- **Raw Telegram credentials.** The bot uses the bot token your tenant admin
  configured to call the Telegram Bot API. It never sees your Telegram session,
  password, or 2FA token.
- **The plaintext of your Moses API key after the linking hop.** During linking,
  the frontend forwards the plaintext key once to the bot backend, which
  encrypts it immediately. The plaintext is not logged and not persisted.
- **Transcribed voice notes.** v0.1.0 rejects voice messages with a polite
  reply. Whisper-based transcription is on the v0.2.0 roadmap; until then no
  voice content is processed.
- **Photos, videos, documents, or other attachments.** v0.1.0 captures only
  Telegram's opaque `file_id` for audit, and does **not** download media
  contents. Multimodal forwarding to Moses Manager is on the roadmap.
- **Cross-tenant data.** Every query is scoped to your tenant. The bot's data
  layer follows the same `tenant_id` discipline as moses-platform-prep.

## What Moses platform stores (for context)

The bot relies on the platform for AI execution. Standard Moses storage
applies — see your tenant's Moses privacy policy. Notably:

- Your conversations with Moses Manager land in `chat_conversations` /
  `chat_messages` on the platform, the same as if you used the in-browser
  chat. The bot does not duplicate them; it stores its own thin audit log
  in `chat_relay_messages`.
- Your MCP API key lives in the platform's `mcp_api_keys` table (SHA-256
  hashed; the platform doesn't keep the plaintext either). The bot stores
  the ciphertext of the plaintext you forwarded — different storage, same
  protection bar.

## Access

- **You** can see your own message history and links in the bot's embedded
  admin UI (Apps → Chat Bot Bridge).
- **Your tenant admin** can read the bot's audit tables (`chat_relay_*`) via
  the Moses admin UI's data-export path, just like any other app's data.
  Admins do **not** have access to plaintext API keys (only ciphertext).
- **GlobalAdmin** can read across tenants via the platform's admin path;
  same constraint — no plaintext keys.

## Data export and deletion

- Export: use the existing Moses data-export path in **User Settings →
  Privacy**. The bot's tables are included.
- Deletion: trigger a GDPR delete from the same panel. The bot honors the
  platform's user-deletion signal by marking your `chat_relay_links` rows
  inactive and queueing a hard-delete of your `chat_relay_messages` and
  `provider_chat_state` rows. (As above, automated retention sweep is a
  v1.1 follow-up; the on-demand delete path is in place.)

## Telegram's own privacy policy

Anything you send through Telegram also lives on Telegram's servers under
Telegram's terms. The bot has no control over that side of the wire.

## Changes

This document is versioned with the bot. When it changes, the version line at
the top moves and the change is summarized in the repo's release notes.
