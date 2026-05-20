// E2E network-level mocks for the moses-chat-bot frontend.
//
// Playwright can't talk to a real bot backend without orchestrating it, so we
// intercept every `/api/v1/*` call (both the bot's own backend AND the
// platform's `/api/v1/api-keys` mint) with deterministic responses.
//
// `setupMockBotAPI(page, opts)` is the high-level entry — call it before
// `page.goto()`. Per-test overrides can be layered via `page.route()` after.

import type { Page, Route } from '@playwright/test';

export interface Link {
  id: string;
  provider: string;
  providerUserId: string;
  providerDisplayName?: string;
  isActive: boolean;
  createdAt: string;
  lastUsedAt?: string | null;
  deactivatedAt?: string | null;
  deactivationReason?: string | null;
}

export interface Message {
  id: string;
  linkId: string;
  direction: 'in' | 'out';
  text: string;
  occurredAt: string;
  error?: string | null;
  metadata?: Record<string, unknown>;
}

export interface MockOpts {
  /** Pre-seeded links returned by GET /api/v1/links. Default []. */
  links?: Link[];
  /** Pre-seeded messages returned by GET /api/v1/messages. Default []. */
  messages?: Message[];
  /**
   * How many poll calls return `pending` before `completed` flips on.
   * Default 1 — the first poll already completes (keeps tests fast).
   */
  pollPendingCount?: number;
  /**
   * linkId surfaced by the eventual completion. Default `link-e2e-001`.
   */
  completedLinkId?: string;
}

export interface MockHandle {
  /** Total number of `DELETE /api/v1/links/:id` calls observed. */
  deletedLinks: string[];
  /** Codes minted via POST /api/v1/links/codes. */
  mintedCodes: string[];
}

const sixtySecondsFromNow = (): string =>
  new Date(Date.now() + 60_000).toISOString();

function jsonBody(value: unknown): { contentType: string; body: string } {
  return { contentType: 'application/json', body: JSON.stringify(value) };
}

/**
 * Install backend mocks on `page`. Returns a handle so tests can assert which
 * URLs were touched (e.g. that DELETE was called).
 */
export async function setupMockBotAPI(page: Page, opts: MockOpts = {}): Promise<MockHandle> {
  const links = [...(opts.links ?? [])];
  const messages = [...(opts.messages ?? [])];
  const pollPendingCount = opts.pollPendingCount ?? 1;
  const completedLinkId = opts.completedLinkId ?? 'link-e2e-001';

  const handle: MockHandle = { deletedLinks: [], mintedCodes: [] };
  // The bot mints a single code per generate; count how many polls have
  // happened so we can flip pending → completed deterministically.
  let pollCalls = 0;

  // ─── Platform: /api/v1/api-keys (mint) and /api/v1/api-keys/:id (revoke) ──
  // Single regex route so glob ambiguity around `*` doesn't bite us.
  // The regex tolerates an optional `?query` tail so query strings don't break
  // matching.
  await page.route(/\/api\/v1\/api-keys(\/[^/?]+)?(\?.*)?$/, (route: Route) => {
    const method = route.request().method();
    const url = new URL(route.request().url());
    const hasId = url.pathname.split('/').filter(Boolean).length === 4; // api/v1/api-keys/<id>
    if (!hasId && method === 'POST') {
      return route.fulfill({
        status: 201,
        ...jsonBody({
          keyId: 'kid-e2e-001',
          key: 'mcp-e2e-deadbeef-never-leaks-to-dom',
        }),
      });
    }
    if (hasId && method === 'DELETE') {
      return route.fulfill({ status: 204, body: '' });
    }
    return route.fulfill({ status: 405, ...jsonBody({ error: 'method_not_allowed' }) });
  });

  // ─── Bot: link codes + links + messages ─────────────────────────────────
  // Single regex route covers every bot endpoint the frontend exercises.
  // We dispatch on (pathname, method) inside the handler so we never trip on
  // Playwright's glob `*` semantics (which only match a single segment).
  await page.route(/\/api\/v1\/(links|messages)(\/[^?]*)?(\?.*)?$/, (route: Route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    const path = url.pathname;
    const parts = path.split('/').filter(Boolean); // [api, v1, links, ...] or [api, v1, messages]

    // POST /api/v1/links/codes
    if (method === 'POST' && parts[2] === 'links' && parts[3] === 'codes' && parts.length === 4) {
      const code = 'a1b2c3';
      handle.mintedCodes.push(code);
      pollCalls = 0;
      return route.fulfill({
        status: 201,
        ...jsonBody({ code, expiresAt: sixtySecondsFromNow() }),
      });
    }

    // GET /api/v1/links/codes/:code
    if (method === 'GET' && parts[2] === 'links' && parts[3] === 'codes' && parts.length === 5) {
      pollCalls += 1;
      if (pollCalls <= pollPendingCount) {
        return route.fulfill({ status: 200, ...jsonBody({ status: 'pending' }) });
      }
      return route.fulfill({
        status: 200,
        ...jsonBody({ status: 'completed', linkId: completedLinkId }),
      });
    }

    // GET /api/v1/links
    if (method === 'GET' && parts[2] === 'links' && parts.length === 3) {
      return route.fulfill({ status: 200, ...jsonBody({ links }) });
    }

    // GET /api/v1/links/:id/messages
    if (method === 'GET' && parts[2] === 'links' && parts[4] === 'messages' && parts.length === 5) {
      return route.fulfill({ status: 200, ...jsonBody({ messages }) });
    }

    // DELETE /api/v1/links/:id
    if (method === 'DELETE' && parts[2] === 'links' && parts.length === 4) {
      const id = parts[3];
      handle.deletedLinks.push(id);
      const idx = links.findIndex((l) => l.id === id);
      if (idx >= 0) links.splice(idx, 1);
      return route.fulfill({ status: 204, body: '' });
    }

    // GET /api/v1/messages
    if (method === 'GET' && parts[2] === 'messages') {
      return route.fulfill({ status: 200, ...jsonBody({ messages }) });
    }

    return route.fulfill({ status: 405, ...jsonBody({ error: 'method_not_allowed' }) });
  });

  return handle;
}

/**
 * Helper to build N synthetic messages alternating in/out. Used by the
 * messages-search spec to populate a deterministic dataset.
 */
export function buildSampleMessages(count: number, linkId = 'l-1'): Message[] {
  const out: Message[] = [];
  const base = Date.parse('2026-05-19T10:00:00Z');
  for (let i = 0; i < count; i += 1) {
    const direction: 'in' | 'out' = i % 2 === 0 ? 'in' : 'out';
    // Mix in a few rows that match the "deploy" search needle.
    const text =
      i % 7 === 0
        ? `deploy succeeded for build ${i}`
        : direction === 'in'
          ? `user said hello ${i}`
          : `bot replied with answer ${i}`;
    out.push({
      id: `m-${i}`,
      linkId,
      direction,
      text,
      occurredAt: new Date(base + i * 1000).toISOString(),
    });
  }
  return out;
}

export const sampleActiveLink: Link = {
  id: 'link-active-001',
  provider: 'telegram',
  providerUserId: '99887766',
  providerDisplayName: 'E2E Tester',
  isActive: true,
  createdAt: '2026-05-19T00:00:00Z',
  lastUsedAt: '2026-05-19T09:00:00Z',
};
