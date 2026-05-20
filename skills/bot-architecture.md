---
name: moses-chat-bot
description: Use when sending messages to a user's mobile chat (Telegram via moses-chat-bot bridge). Covers when to push, tone, batching, and which workspace tool to call.
---

# moses-chat-bot — Pushing to a User's Mobile Chat

The `moses-chat-bot` app is a bridge: Telegram (today; Discord/Slack later) inbound becomes a Moses Manager conversation under the user's API key, and outbound MM-side notifications fan out to the same Telegram chat via this app's workspace-tool surface. Use this skill when you (Moses Manager, an execution agent, or any caller with the workspace tools) want to ping a user on their phone.

## The four workspace tools

The bot exposes OpenAPI at `/api/openapi.json`, which the platform auto-discovers and prefixes with `workspace_moses_chat_bot_`. You get exactly four tools:

| Tool | When to call |
|------|--------------|
| `workspace_moses_chat_bot_pushMessage` | Fan-out to **all** of a single user's active links. Use when you know the `moses_user_id` and want every device that user linked to buzz. This is the workhorse for terminal events: deploy succeeded, ticket DONE, approval needed. |
| `workspace_moses_chat_bot_listLinks` | Discovery. Use when you don't already know which users are linked — e.g. before a tenant-wide license-expiry blast. Returns active links for the calling tenant only. |
| `workspace_moses_chat_bot_notifyLink` | Single-link send by `link_id`. Cheaper than `pushMessage` because it skips the user-to-links lookup. Use when you already have the link from `listLinks` or a previous push. |
| `workspace_moses_chat_bot_listRecentMessages` | Read up to the last 100 relay turns for a user. Use for context-aware follow-ups ("you asked me about X yesterday — that build just succeeded"). |

Tenant safety: every call is already scoped to the calling tenant by the workspace-tool wedge. You cannot push to a link in a different tenant even if you know its ID.

## When to push, and when not to

**Push for:**
- Terminal deploy outcomes the user is waiting on — succeeded or failed.
- Human-approval-required states blocking forward progress.
- Agent-pod completion for work the user explicitly kicked off.
- Autopilot session start, completion, or failure.
- Security or license alerts (key revoked, license expiring this week, quota hit).
- Hard errors that block the user from continuing.

**Don't push for:**
- Low-significance events: lane moved, label added, comment edited, ticket reordered.
- Repeated identical states — if you just pushed "build started" don't push "build still running" 30s later.
- Anything the user just did in-app: they are already there; the UI told them.
- Bulk events without aggregation. If five tickets transitioned, send **one** message that summarises them, not five.

One-line rule of thumb: **would the user thank you for the buzz at 11pm?** If not, log it, surface it in-app, or wait until you can aggregate.

## Tone

Telegram is a mobile messenger. The user sees these on a lock screen at the gym. Optimise for skim.

- One short subject in **bold** + the key fact + at most one follow-up sentence.
- No top-level headings (`#`, `##`). They render as plain text on most clients.
- No nested lists. Inline what matters.
- Markdown that works everywhere: `**bold**`, `*italic*`, backtick code. Anything else is risky.
- Include a path / chart / ticket reference the user can pattern-match (`/apps/acme/my-app`, `CHAT-7xy2`).
- No emoji unless the user has opted in via settings. Default off.

**Good:**
```
**Deploy succeeded:** my-app v1.2.3 is live at /apps/acme/my-app. Build took 4m12s.
```

```
**Approval needed:** CHAT-7xy2 wants to launch an execution agent. Approve in Agents → Pending Approvals.
```

**Bad:**
```
# 🚀 Deployment Update

## Status

Your deployment of my-app version 1.2.3 has completed successfully. The build phase took...
```

Too much chrome, eats screen real estate, ignores the medium.

## Privacy and tenant safety

The bot's auth model already prevents cross-tenant pushes, but **content** is still on you:

- Never include another tenant's user names, ticket descriptions, chart contents, or repo paths.
- Don't echo the user's own message text back at them. It's already in their chat history; you're just adding noise.
- Don't include URLs that point at other-tenant resources, even if you can format them.
- Don't include API keys, OAuth tokens, signing secrets, encryption keys, or webhook secrets — even if they show up in a tool result you're summarising. Strip them. If you're unsure whether a token is sensitive, treat it as sensitive.
- Don't include the user's full email or phone unless they wrote it themselves and you're reflecting it back in context.

## Batching and rate limits

The bot's outbound bucket is **30 messages per minute per link**. Treat that as a hard ceiling, not a target.

- Aggregate multi-event scenarios into one push. "Three deployments completed: my-app, billing-api, web" is one message; three separate pushes is three buzzes and an annoyed user.
- For a high-frequency event source, prefer `listRecentMessages` + a digest over real-time fan-out.
- Don't retry on the rate-limited response in a tight loop. Back off, aggregate, send once.
- The bot also gates its own outbound when the user has `/dnd` active. You don't need to suppress separately — just send normally; the bot drops or queues per the user's settings.

## Common patterns

**Push to one user on ticket DONE.** You have the `moses_user_id` from the ticket's `completed_by_user_id`:

```
workspace_moses_chat_bot_pushMessage({
  moses_user_id: "<user-uuid>",
  text: "**Ticket DONE:** CHAT-7xy2 (Refactor auth middleware). Merge succeeded; my-app v1.4.0 deploying now.",
  markdown: true
})
```

**Notify all linked users about an upcoming license expiry** (tenant-wide blast):

```
const { links } = workspace_moses_chat_bot_listLinks({ active_only: true })
for (const link of links) {
  workspace_moses_chat_bot_notifyLink({
    link_id: link.id,
    text: `**Heads up:** your tenant's license expires in 5 days. Renew in Tenant Admin → License.`,
    markdown: true
  })
}
```

Use `notifyLink` in the loop, not `pushMessage` per user — you already have the link IDs, so skip the lookup.

**Context-aware follow-up.** You're answering a question and want to reference the user's recent context:

```
const { messages } = workspace_moses_chat_bot_listRecentMessages({
  moses_user_id: "<user-uuid>",
  limit: 20
})
// Inspect for the topic the user asked about, then push the answer
workspace_moses_chat_bot_pushMessage({
  moses_user_id: "<user-uuid>",
  text: "**Update on the migration you asked about:** schema 951 applied cleanly on prod. No rollback needed.",
  markdown: true
})
```

**Deploy-failed escalation.** Tone shifts to urgent + actionable, but stays short:

```
workspace_moses_chat_bot_pushMessage({
  moses_user_id: "<user-uuid>",
  text: "**Deploy failed:** my-app build phase errored — `compile error: backend/internal/services/foo.go:42`. Logs in Moses Git → Executions → exec abc12345.",
  markdown: true
})
```

## What this skill is NOT

- Not a replacement for the in-Moses UI. The bot's frontend at `/apps/<tenant>/moses-chat-bot/` is where users manage their own links, view relay history, and configure DND/notification preferences — you don't push *to* the frontend, the user opens it themselves.
- Not a way to change inbound chat behaviour. When the user types into Telegram, the bot routes them into Moses Manager via their API key. That path is the normal MM tool surface and has nothing to do with this skill.
- Not a fallback channel for things you couldn't say in-chat. If you're answering a question in Moses Manager, answer it in Moses Manager. Pushing to the user's phone is for events they aren't currently watching for.
- Not a metrics or analytics surface. If you need delivery stats, the bot's own admin frontend has them; don't try to reconstruct them from `listRecentMessages`.
