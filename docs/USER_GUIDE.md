# User Guide

Everything you need to use Moses from your phone via Telegram.

## Getting started

Prerequisites:

- Your Moses tenant has **Chat Bot Bridge** installed. If you can't find it in
  the Apps grid, ask your tenant admin to install it
  ([Admin Guide](./ADMIN_GUIDE.md)).
- You can log in to Moses normally (the bot reuses your session to mint your
  personal API key).
- You can use AI in Moses (the `USE AI` permission, identical to the gate for
  the in-browser Moses Manager).
- A Telegram account on your phone.

### Linking your Telegram

1. Open Moses → **Apps** → **Chat Bot Bridge**.

   ![Dashboard with no links](../screenshots/dashboard.png)

2. Click **Link new chat**, pick **Telegram**.

   ![Link new chat](../screenshots/link-new.png)

3. The UI displays a 6-digit code with a 60-second countdown. The plaintext API
   key never re-enters the page after this hop — it's encrypted at rest in the
   bot's database.

4. On your phone, open Telegram and start a conversation with your tenant's bot
   (your admin will tell you the bot's @-handle).

5. Send `/start` once to register your Telegram user.

6. Send `/link 123456` (replace with your code). The bot confirms:

   ```text
   Linked successfully. Talk to me anytime.
   ```

7. Back in the bot's web UI, the new link appears in your dashboard. You can
   open it for chat history and settings.

   ![Link detail](../screenshots/link-detail.png)

On a phone-sized screen the UI compacts to a single column:

![Mobile dashboard](../screenshots/mobile-dashboard.png)
![Mobile link new](../screenshots/mobile-link-new.png)

## Sending messages

Once linked, the bot relays anything you type to your Moses Manager. The full
~84-tool MM surface is available — you can ask it to read tickets, run queries,
draft commits, kick off deployments, anything you do in the in-browser chat.

```text
You:    What tickets are in REVIEW right now?
Bot:    [streams MM response, then sends one or more Telegram messages]
```

![Telegram chat](../screenshots/telegram-chat.png)

Long responses are split at word boundaries to stay under Telegram's 4096-char
cap. The bot persists each turn pair so you can scroll relay history in the
admin UI.

Conversation reuse: each Telegram chat keeps the same Moses conversation across
turns, so MM has continuity. Use `/clear` to start fresh.

## Slash commands

| Command | What it does |
|--------|--------------|
| `/start` | Welcome message + linking instructions. Send this once before `/link`. |
| `/link <code>` | Complete linking using the 6-digit code from the web UI. |
| `/unlink` | Soft-unlink: marks your link inactive but preserves history. |
| `/help` | List commands. |
| `/use <tenant_slug>` | For multi-tenant users, switch active tenant. |
| `/tickets` | Show your open tickets (calls MM with a structured prompt). |
| `/status` | Show platform status and your active autopilot session, if any. |
| `/autopilot start` | Start an autonomous session for your tenant. |
| `/autopilot stop` | Halt your active autopilot session. |
| `/autopilot status` | Show current autopilot session state. |
| `/clear` | Start a fresh Moses conversation (new chat row). |
| `/dnd <duration>` | Snooze outbound pushes for N hours (e.g. `/dnd 4h`). |

Anything that isn't a `/` command is forwarded to Moses Manager as a regular
chat message.

## Autopilot from Telegram

Autopilot picks the next ready ticket and works it autonomously. From Telegram:

- `/autopilot start` — Asks the platform to start a session. The platform
  enforces a **per-tenant singleton**: if another user is already running
  autopilot in your tenant, your start is **refused** with a clear message
  ("Tenant already has an active autopilot session owned by another user; ask
  them to stop it first."). If you're the existing owner, the bot returns the
  current session info instead of starting a new one.
- `/autopilot stop` — Halts your active session.
- `/autopilot status` — Returns the session's state, started-at, and current
  ticket. Same data as the dashboard's autopilot card.

  ![Autopilot status](../screenshots/autopilot-status.png)

When a session reaches a terminal state (`completed`, `cancelled`, or `failed`),
the bot's 60-second sweeper notices and DMs you a one-line summary. You don't
need to leave the app open.

You need the platform's `CREATE AUTONOMOUS_SESSIONS` permission to use
autopilot; the bot does not grant it.

## Settings

Open **Chat Bot Bridge** → **Settings** to configure:

![Settings](../screenshots/settings.png)

- **Notifications**: which event types you want push notifications for
  (deploy success / failure, ticket transitions, approval requests).
- **Do-not-disturb hours**: a daily window during which the bot holds outbound
  pushes. Inbound (you → MM) is never suppressed.
- **Autopilot defaults**: pre-fills the params for `/autopilot start`.

**These settings are advisory.** The bot stores them per-user in your browser's
`localStorage`, and Moses Manager's chat-bot skill reads them when deciding
what to push. They don't survive switching browsers or devices — cross-device
sync is planned for a later release. If you reinstall your browser, you'll
re-do these.

## Privacy

What the bot stores about you:

- Your **encrypted Moses API key** (AES-256-GCM, tenant-derived DEK).
- Your **Telegram user ID**, paired with your Moses user ID.
- **Message text** you sent and received via the bot, persisted for audit. A
  retention sweep is a v1.1 follow-up; today, history accumulates.
- The **Moses conversation IDs** linked to each chat.

What the bot does **not** store:

- Your raw Telegram credentials. The bot talks to Telegram via the bot token
  your admin configured — never your Telegram session.
- Photos or attachments. v1 captures only the Telegram `file_id` for audit;
  no media is downloaded.
- Transcribed voice. Voice notes are politely rejected in v1; Whisper-based
  transcription is in the v0.2.0 roadmap.

For data-export or deletion, use the existing Moses data-export path in your
user settings. Full data-handling details: [PRIVACY.md](./PRIVACY.md).

## Troubleshooting

**"Your Moses key was revoked"**

The bot detected a 401 from the platform on your behalf. Something invalidated
your key (you or an admin revoked it in Moses UI, or master-key rotation hit a
ciphertext under an unavailable master version). Re-link from the web UI.

**"Send /start first"**

Telegram doesn't let bots message you until you've initiated. Send `/start` to
the bot, then try `/link <code>` again.

**"No autopilot session"**

You ran `/autopilot stop` or `/autopilot status` without an active session.
Start one with `/autopilot start` first.

**"Tenant already has an active autopilot session owned by another user"**

Another user in your tenant is running autopilot. Coordinate with them (they
can `/autopilot stop`), or wait for their session to finish.

**Code expired**

The 6-digit linking code is valid for 60 seconds. Click **Link new chat** again
to mint a fresh one.

**3 failed `/link` attempts**

The bot locks out further attempts from your Telegram account for 15 minutes.
Wait, then mint a fresh code (the old one has expired anyway).

**Telegram says "this bot has no messages"**

You haven't sent `/start` yet — Telegram hides messages from bots you haven't
contacted. Send `/start` once and the relay starts working.

**My response was cut off**

Telegram caps messages at 4096 chars; longer MM replies are split into multiple
messages on word boundaries. Scroll back — the rest is right above.

## FAQ

**Can I link more than one Telegram account?**

One active link per (Moses user, Telegram user) pair. You can `/unlink` and
re-link with a different Telegram account; history is preserved.

**Can two Telegram accounts share one Moses user?**

No — the link is uniquely keyed on `(provider, provider_user_id)`. Each Telegram
account links once.

**Does the bot see my plaintext API key?**

The backend sees it once during linking, encrypts it with AES-256-GCM under a
tenant-derived DEK, stores the ciphertext, and never logs the plaintext. On
every relay, the bot decrypts in-memory and forwards as a bearer token.

**What happens if I uninstall the app from my tenant?**

Your active link rows are soft-deleted on uninstall. Your API key in Moses
remains until you revoke it from Moses UI — uninstalling the bot does not
auto-revoke. Re-installing later requires re-linking; ciphertexts encrypted
under the previous master key cannot be reused.

**Can the bot read my other Moses chats?**

Only via the same API surface your in-browser chat uses. Your API key carries
your permissions and nothing more; there's no impersonation or elevated access.
