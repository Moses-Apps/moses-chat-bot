// Typed wrappers around the Moses platform endpoints the linking flow
// needs to touch directly (SPEC.md §4 step 3).
//
// The frontend runs inside the iframe at /apps/<tenant>/moses-chat-bot/ which
// is same-origin with the platform, so the cookie auth carries over. We use a
// dedicated axios instance with baseURL anchored at the platform host so the
// app's own `/api/v1` (bot backend) and the platform's `/api/v1` (api-keys)
// don't collide.

import axios, { type AxiosInstance } from 'axios';
import { attachInterceptors } from './api';

export interface CreateMcpKeyRequest {
  name: string;
  /** Allow-listed by the platform: 'external-minimal' | 'moses-manager-full'. */
  profile: 'external-minimal' | 'moses-manager-full';
  /** ISO-8601 timestamp. */
  expiresAt?: string;
}

export interface CreateMcpKeyResponse {
  keyId: string;
  /** Plaintext key — surfaced once, never refetchable. */
  key: string;
}

// Platform mounts under the same origin in the iframe; an env override exists
// for local dev where the bot frontend might be served standalone.
function resolvePlatformBase(): string {
  const env = (import.meta as { env?: Record<string, string | undefined> }).env;
  return env?.VITE_PLATFORM_API_BASE ?? '/api/v1';
}

const platform: AxiosInstance = attachInterceptors(
  axios.create({
    baseURL: resolvePlatformBase(),
    withCredentials: true,
    headers: { 'Content-Type': 'application/json' },
  }),
);

/**
 * Mint a new MCP API key for the current user (SPEC.md §4 step 3).
 *
 * Requires the platform-side gap-close in SPEC.md §8: `CreateUserAPIKeyHandler`
 * must accept `profile` with RBAC gating. Until shipped, `moses-manager-full`
 * will 400.
 */
export async function createMcpKey(
  request: CreateMcpKeyRequest,
): Promise<CreateMcpKeyResponse> {
  const { data } = await platform.post<{
    id?: string;
    keyId?: string;
    api_key_id?: string;
    key?: string;
    api_key?: string;
  }>('/api-keys', request);

  // The platform's response shape uses snake_case in some legacy paths and
  // camelCase elsewhere; normalize both.
  const keyId = data.keyId ?? data.id ?? data.api_key_id ?? '';
  const key = data.key ?? data.api_key ?? '';
  if (!keyId || !key) {
    throw {
      status: 500,
      code: 'invalid_platform_response',
      message: 'Platform did not return a key id + plaintext key',
    };
  }
  return { keyId, key };
}

/** Best-effort revoke of a previously minted MCP key (SPEC.md §4 step 9 unlink). */
export async function revokeMcpKey(keyId: string): Promise<void> {
  await platform.delete(`/api-keys/${encodeURIComponent(keyId)}`);
}

// Exported for testing.
export const _internal = { platform };
