# moses-chat-bot

Bridge your authenticated Moses Manager chats to Telegram (Discord and Slack coming
soon). Send Telegram messages, get streamed Moses Manager responses with the full
~84-tool surface. Moses Manager can also push status and completion notifications
back to you via a workspace tool. Auth is per-user MCP API keys with the
`moses-manager-full` profile (Type C model). The bot does not impersonate — your
key carries your permissions natively.

## Status

- Version: **v0.1.0**
- Providers shipped: **Telegram** only.
- Distribution: marketplace-publishable as a first-party Moses app.
- Platform requirement: moses-platform-prep with the `profile` field accepted by
  `CreateUserAPIKeyHandler` (see [SPEC.md §8](./SPEC.md#8-platform-gap-close-pr-against-moses-platform-prep)).

## Quick start (tenant admin)

1. Install from the Moses marketplace.

   ```text
   Moses UI → Apps → Marketplace → Chat Bot Bridge → Install
   ```

2. Create a Telegram bot via [BotFather](https://t.me/BotFather), copy the token,
   and store it as a K8s Secret in your tenant's namespace.

   ```bash
   kubectl create secret generic telegram-bot-token \
     --namespace=<tenant-ns> \
     --from-literal=TELEGRAM_BOT_TOKEN=<token-from-botfather> \
     --from-literal=TELEGRAM_WEBHOOK_SECRET=$(openssl rand -hex 32)
   ```

   Then add the secret name to the Helm values: `secrets.secretNames: [telegram-bot-token]`.

3. In the Moses admin UI, approve the bot's `moses-platform` integration grant
   (scopes: `chat_relay`, `workspace_push`). Without this, Moses Manager cannot
   push messages back.

4. Tell your users they can now open **Chat Bot Bridge** from `/apps` and link
   their Telegram. Full user flow lives in [docs/USER_GUIDE.md](./docs/USER_GUIDE.md).

See [docs/ADMIN_GUIDE.md](./docs/ADMIN_GUIDE.md) for secret rotation, webhook setup,
and uninstall semantics.

## Quick start (user)

1. Open **Chat Bot Bridge** from the Apps grid in Moses.
2. Click **Link new chat** → pick Telegram.
3. Copy the 6-digit code (valid for 60 seconds).
4. In Telegram, send `/start` to your tenant's bot once, then `/link <code>`. Done —
   type anything and the bot relays it to Moses Manager.

Full walkthrough: [docs/USER_GUIDE.md](./docs/USER_GUIDE.md).

## Architecture

```text
Telegram client
      │   (HTTPS)
      ▼
Telegram Bot API
      │   webhook POST (X-Telegram-Bot-Api-Secret-Token verified)
      ▼
moses-chat-bot backend (this app)
      │   1. Resolve link → decrypt user API key
      │   2. POST /api/v1/ai/chat/stream  (Bearer <user key>)
      │   3. Subscribe to /api/v1/ai/ws    (Bearer <user key>)
      ▼
moses-backend (in-cluster)
      │   Auth → RBAC → MM session pod runs the ~84-tool surface
      │   Streams assistant_message_chunk events back over the WS
      ▼
moses-chat-bot backend
      │   Aggregates chunks → chunks the final text at 4096 chars
      ▼
Telegram Bot API → user's phone
```

Push direction (Moses Manager → user) goes the other way: MM auto-discovers
`workspace_moses_chat_bot_pushMessage` from the bot's OpenAPI spec and calls it
with the user-scoped `MOSES_PLATFORM_API_KEY`.

The push surface (`/api/v1/push/*` + `/api/v1/workspace/*`) is gated by a
constant-time bearer check against `MOSES_PLATFORM_API_KEY`. Without that env
var mounted, every request fails-closed (503). For local development without
a platform pod, set `BOT_PLATFORM_AUTH_DISABLED=true` — the bot will then log
a WARN on every workspace-tool request. See
[docs/ADMIN_GUIDE.md § Workspace-tool surface authentication](./docs/ADMIN_GUIDE.md#workspace-tool-surface-authentication).

Full design in [SPEC.md](./SPEC.md).

## What's in the box

- Inbound relay: Telegram → Moses Manager streaming with WS-backed multi-tool loops.
- Outbound push: workspace-tool OpenAPI surface (`/api/v1/push/message`,
  `/api/v1/links`, `/api/v1/links/:id/notify`, `/api/v1/messages`).
- Autopilot control from Telegram via `/autopilot start|stop|status`, with the
  tenant-singleton constraint enforced and a one-line summary DMed at session end.
- Linking flow: 60-second 6-digit codes minted in the bot's embedded admin UI,
  AES-256-GCM-encrypted per-user keys with tenant-derived DEKs.
- Conversation persistence: messages logged to PostgreSQL for audit and per-chat
  Moses conversation reuse.
- Embedded admin UI (React + Vite + Tailwind, mobile-ready down to 320px) for
  managing links, viewing relay history, and per-user settings.
- Slash commands: `/start /link /unlink /help /tickets /status /autopilot /clear /dnd`.

## Build & test

No deploy required for verification.

```bash
cd backend && go vet ./... && go test ./... && go build ./...
cd frontend && npm install && npm run lint && npm test -- --run && npm run build
```

Or via the top-level `Makefile`:

```bash
make build          # backend + frontend
make test           # backend + frontend test suites
make lint           # go vet + npm run lint
make ci-test        # full CI parity (race, cover, staticcheck)
```

## License

Apache-2.0. Matches the rest of the Moses default-app templates.

## Roadmap

- v0.2.0 — Voice-in via Whisper sidecar; multi-turn history accumulation per chat.
- v0.3.0 — Discord adapter.
- v0.4.0 — Slack adapter.
- v0.5.0 — Cross-device settings sync (settings move out of localStorage into the
  bot's database, scoped by user).
- v0.6.0 — Server-side message search filters (today's frontend filters are
  client-side over the last 100 turns).
