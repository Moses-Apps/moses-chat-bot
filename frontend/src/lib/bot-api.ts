// Typed wrappers around the moses-chat-bot backend (SPEC.md §4, §7).

import api from './api';

export interface CreateLinkCodeRequest {
  /** Plaintext MCP key minted via platform.createMcpKey(). */
  apiKey: string;
  /**
   * UUID of the freshly minted key so the bot can call DELETE /api-keys/:id
   * on unlink. Optional; the backend validates UUID shape when present.
   */
  apiKeyIdHint?: string;
  /** Seconds the 6-digit code remains valid. Server caps at 300s. */
  expiresInSeconds: number;
}

export interface CreateLinkCodeResponse {
  /** 6-digit code shown to the user; opaque to the frontend. */
  code: string;
  /** ISO-8601 timestamp when this code expires. */
  expiresAt: string;
}

export type LinkCodeStatus = 'pending' | 'completed' | 'expired';

export interface PollLinkCodeResponse {
  status: LinkCodeStatus;
  /** Present only when status === 'completed'. */
  linkId?: string;
}

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

/** Create a 6-digit linking code (SPEC.md §4 step 4). */
export async function createLinkCode(
  request: CreateLinkCodeRequest,
): Promise<CreateLinkCodeResponse> {
  const { data } = await api.post<CreateLinkCodeResponse>('/links/codes', request);
  return data;
}

/** Poll a link code's status until completion/expiry (SPEC.md §4 step 5/8). */
export async function pollLinkCode(code: string): Promise<PollLinkCodeResponse> {
  const { data } = await api.get<PollLinkCodeResponse>(
    `/links/codes/${encodeURIComponent(code)}`,
  );
  return data;
}

/** List the active links for the current user. */
export async function listLinks(): Promise<Link[]> {
  const { data } = await api.get<{ links: Link[] } | Link[]>('/links');
  return Array.isArray(data) ? data : data.links;
}

/** Recent relay history for a single link (last N turns). */
export async function getLinkMessages(linkId: string, limit = 100): Promise<Message[]> {
  const { data } = await api.get<{ messages: Message[] } | Message[]>(
    `/links/${encodeURIComponent(linkId)}/messages`,
    { params: { limit } },
  );
  return Array.isArray(data) ? data : data.messages;
}

/** Soft-unlink (SPEC.md §12 /unlink). */
export async function deleteLink(linkId: string): Promise<void> {
  await api.delete(`/links/${encodeURIComponent(linkId)}`);
}

/** Filter set for the global messages search page. */
export interface SearchMessagesParams {
  /** Restrict to a single link. When omitted, returns messages across all of the user's links. */
  linkId?: string;
  /** 'in' (user → bot), 'out' (bot → user), or 'all'/undefined. */
  direction?: 'in' | 'out' | 'all';
  /** Free-text needle. Lower-cased substring match (client-side for v1). */
  q?: string;
  /** ISO date (YYYY-MM-DD). Inclusive lower bound on `occurredAt`. */
  dateFrom?: string;
  /** ISO date (YYYY-MM-DD). Inclusive upper bound on `occurredAt`. */
  dateTo?: string;
  /** Page size. Default 50. Server may cap. */
  limit?: number;
  /** Opaque cursor returned by a prior page. */
  cursor?: string;
}

export interface SearchMessagesResponse {
  messages: Message[];
  /** Present iff there's at least one more page server-side. */
  nextCursor?: string;
}

/**
 * Cross-link message search.
 *
 * The backend endpoint at `/api/v1/messages` accepts `link_id`, `limit`, and
 * `cursor` today; the remaining filters (direction, full-text, date range)
 * are applied client-side after fetch. This is a v1 simplification.
 *
 * TODO(server-side filters): a follow-up bead tracks moving direction/q/date
 * into the backend so we don't have to keep paging through irrelevant rows.
 * Search code comments for "server-side message search" or see beads.
 */
export async function searchMessages(
  params: SearchMessagesParams = {},
): Promise<SearchMessagesResponse> {
  const { linkId, limit = 50, cursor } = params;
  const query: Record<string, string | number> = { limit };
  if (linkId) query.link_id = linkId;
  if (cursor) query.cursor = cursor;
  const { data } = await api.get<SearchMessagesResponse | Message[]>(
    '/messages',
    { params: query },
  );
  // Accept both legacy bare-array and {messages, nextCursor?} envelopes.
  if (Array.isArray(data)) return { messages: data };
  return { messages: data.messages ?? [], nextCursor: data.nextCursor };
}
