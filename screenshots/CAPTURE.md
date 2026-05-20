# Screenshots — capture recipe

The PNG files in this directory are **1x1 transparent placeholders** for v0.1.0. The
markdown files in `docs/` reference them by name so the layout is final; only the
images need replacing. A v1.1 follow-up captures real screenshots from a running
local deploy and commits them in place.

## Why placeholders

Capturing real screenshots requires:

- A live Moses platform install (Minikube + Postgres + Keycloak).
- The bot installed via marketplace with `moses-platform` integration approved.
- A real Telegram bot via BotFather and a test linked Telegram account.

That stack is out of reach from a CI-style sub-agent. The recipe below documents
what someone with a local install should run.

## Recipe

Prerequisites:

- `moses-platform-prep` deployed locally per its `CLAUDE.md` quick start.
- `moses-chat-bot` installed in your tenant via marketplace (see `docs/ADMIN_GUIDE.md`).
- A linked Telegram account.
- Playwright already installed (`@playwright/test`); the bot repo's frontend tests
  set this up.

```bash
# From the moses-chat-bot repo root
cd frontend && npm run build && npm run preview &
PREVIEW_PID=$!

# Wait for preview to be ready on http://localhost:4173
until curl -fsS http://localhost:4173/ >/dev/null; do sleep 1; done

# Drive captures with Playwright. Save into ../screenshots/.
npx playwright screenshot --viewport-size=1280,800 http://localhost:4173/      ../screenshots/dashboard.png
npx playwright screenshot --viewport-size=1280,800 http://localhost:4173/link/new ../screenshots/link-new.png
npx playwright screenshot --viewport-size=1280,800 "http://localhost:4173/links/<id>" ../screenshots/link-detail.png
npx playwright screenshot --viewport-size=1280,800 http://localhost:4173/settings ../screenshots/settings.png

# Mobile views
npx playwright screenshot --viewport-size=375,812  http://localhost:4173/      ../screenshots/mobile-dashboard.png
npx playwright screenshot --viewport-size=375,812  http://localhost:4173/link/new ../screenshots/mobile-link-new.png

kill $PREVIEW_PID
```

Telegram chat capture (`telegram-chat.png`) and the autopilot status capture
(`autopilot-status.png`) must be taken from a real Telegram client and from the
bot's `/autopilot status` reply. Use the OS screenshot tool, crop, and save with
the exact filename.

## Filenames referenced

These names are stable; if you rename one, also update every reference in
`README.md` and `docs/`:

- `dashboard.png`
- `link-new.png`
- `link-detail.png`
- `telegram-chat.png`
- `mobile-dashboard.png`
- `mobile-link-new.png`
- `autopilot-status.png`
- `settings.png`
