# moses-chat-bot — Developer Quick Reference

> First-party Moses app that bridges Moses Manager chats to mobile messengers (Telegram first; Discord/Slack later). Lives at `../moses-chat-bot` in the Moses ecosystem workspace. Built from the `fullstack-chat` template pattern.

## What this is

A self-contained marketplace app. Each tenant installs it once; per-user links via a `/link <code>` flow from inside Moses UI. Auth model is Type C (per-user MCP API keys with `moses-manager-full` profile) — the bot does not impersonate; the user's API key carries their permissions natively.

Inbound (Telegram → Moses): webhook → resolve link → forward as the user's bearer token to `POST /api/v1/chat/conversations/:id/messages` on moses-backend → stream reply back to Telegram.

Outbound (Moses → user): bot exposes OpenAPI `/api/v1/push/message`. Moses Manager auto-discovers it as `workspace_moses-chat-bot_pushMessage` and uses it for completion / status notifications.

**Read SPEC.md first.** It's the canonical design. CLAUDE.md only points at it.

## Tech stack

- Backend: Go 1.24 + standard library HTTP + sqlc-generated queries against PostgreSQL.
- Frontend: React 19 + TypeScript + Vite + Tailwind + Zustand.
- Container: same multi-stage pattern as `fullstack-chat`.
- Helm: minimal chart (deployment + service + ingress + postgres via `dependencies`).

## Repo layout (planned)

```
moses-chat-bot/
├── SPEC.md                    # Canonical design (READ THIS)
├── CLAUDE.md                  # This file
├── README.md
├── moses-app.config.json      # App metadata + workspace-tool registration
├── .beads/                    # Beads tracker (issue prefix: moses-chat-bot)
├── backend/
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── config/            # MOSES_* + bot-specific env vars
│   │   ├── db/                # PostgreSQL access (sqlc)
│   │   ├── handler/           # HTTP handlers (webhook, push API, links, etc.)
│   │   ├── service/
│   │   │   ├── linker/        # Linking flow + key minting via moses-backend
│   │   │   ├── relay/         # Inbound + outbound message routing
│   │   │   ├── autopilot/     # Autonomous session helpers
│   │   │   └── crypto/        # AES-256-GCM key envelope
│   │   ├── provider/
│   │   │   ├── provider.go    # Interface
│   │   │   ├── registry.go    # Active provider list
│   │   │   └── telegram/      # Adapter
│   │   ├── mosesclient/       # Typed wrapper around moses-backend API
│   │   └── mosesproxy/        # Vendored from fullstack-chat (iframe SDK proxy)
│   ├── postgresql/schema/     # Numbered .sql files (001_, 002_, ...)
│   └── api/openapi.json       # Workspace-tool OpenAPI spec
├── frontend/
│   ├── src/
│   │   ├── pages/             # Dashboard / LinkNew / LinkDetail / Messages / Settings
│   │   ├── components/        # Bento layout + shared UI
│   │   ├── lib/api.ts         # axios + iframe-SDK
│   │   └── stores/            # Zustand
│   └── ...
├── helm/                      # Chart + templates
└── skills/                    # bot-architecture.md (injected into agent pods)
```

## Build verification (no deploy needed)

```bash
cd backend && go vet ./... && go test ./... && go build ./...
cd frontend && npm install && npm run lint && npm test -- --run && npm run build
```

## Deploy locally

Once moses-platform-prep is running and the bot is registered as a workspace tool:

```bash
# From the bot repo
make deploy-local   # builds local images, helm upgrade --install
```

(Make target lands as part of T-INFRA-1.)

## Coding standards

Same as moses-platform-prep's `coding-standards/MOSES_BACKEND_STANDARDS.md` and `MOSES_UI_UX_STANDARDS.md`. In particular:
- Tenant isolation on every query (`tenant_id` always in WHERE).
- No comments-as-narration (well-named identifiers do the talking).
- Forward-only schema migrations (numbered `.sql` files; never edit applied DDL).
- 4px spacing grid + WCAG 2.1 AA accessibility.

## Beads workflow

This repo uses beads with prefix `moses-chat-bot-*`. Common loop:

```bash
bd ready                    # find available work
bd show <id>                # see acceptance criteria
bd update <id> --status=in_progress
# ...do the work...
bd close <id>
bd sync                     # commit beads changes
```

## Platform changes required

A single narrow PR against moses-platform-prep adds a `profile` field to `CreateUserAPIKeyHandler`. Tracked in `moses-platform-prep/.beads/` (look for issues prefixed with `BOT-` or referencing `chat-bot`). The bot does not work until that PR ships.
