// Shared axios instance for moses-chat-bot frontend.
//
// - baseURL defaults to '/api/v1' (same-origin to the app's own backend), but
//   `platform.ts` overrides per-call to talk to the Moses platform.
// - withCredentials: true is required so the platform's `access_token` cookie
//   travels along on cross-origin same-host iframe calls (SPEC.md §4).
// - Request interceptor stamps X-Requested-With when inside an iframe — the
//   backend can use that to distinguish embedded vs standalone clients.
// - Response interceptor unwraps server errors into a structured
//   { status, code, message } shape so handlers don't have to fish through
//   Axios internals.

import axios, {
  AxiosError,
  AxiosHeaders,
  type AxiosInstance,
  type AxiosResponse,
  type InternalAxiosRequestConfig,
} from 'axios';
import { isEmbedded } from './iframe';
import { mosesBasePath } from './basePath';

export interface ApiError {
  status: number;
  code: string;
  message: string;
  /** Original axios error, retained for debugging. */
  cause?: unknown;
}

function resolveBaseURL(): string {
  // import.meta.env is statically replaced by Vite at build time; the optional
  // chain guards tests that mock a bare environment.
  const env = (import.meta as { env?: Record<string, string | undefined> }).env;
  if (env?.VITE_API_BASE) return env.VITE_API_BASE;
  // The bot's OWN backend is reachable only under the app's runtime deploy
  // prefix — Moses routes /apps/<tenant>/<app>/* to this app's nginx. A bare
  // '/api/v1' resolves against the iframe origin root and hits the Moses
  // gateway instead (404). Prefix with the runtime base path.
  return `${mosesBasePath()}/api/v1`;
}

const MUTATING_METHODS = new Set(['post', 'put', 'patch', 'delete']);

// Moses guards cookie-authed mutating requests with a double-submit CSRF
// token. The csrf_token cookie is HttpOnly + SameSite=Strict, so JS cannot
// read it — Moses instead echoes the current token in the `X-Csrf-Token`
// RESPONSE header (including on the 403 it returns when the header is
// missing). We cache that value and send it back as `X-CSRF-Token`.
let csrfToken: string | null = null;

function captureCsrf(headers: unknown): void {
  const h = headers as { 'x-csrf-token'?: string } | undefined;
  const token = h?.['x-csrf-token'];
  if (typeof token === 'string' && token.length > 0) {
    csrfToken = token;
  }
}

function toApiError(error: AxiosError): ApiError {
  const status = error.response?.status ?? 0;
  const data = error.response?.data as
    | { code?: string; error?: string; message?: string }
    | undefined;
  return {
    status,
    code: data?.code ?? (status ? `http_${status}` : 'network_error'),
    message: data?.message ?? data?.error ?? error.message ?? 'Request failed',
    cause: error,
  };
}

type RetriableConfig = InternalAxiosRequestConfig & { __csrfRetried?: boolean };

/**
 * Apply the moses-chat-bot interceptors to any axios instance. Exported for
 * testing; production code uses the pre-configured `api` singleton below.
 */
export function attachInterceptors(instance: AxiosInstance): AxiosInstance {
  instance.interceptors.request.use((config: InternalAxiosRequestConfig) => {
    // Some axios versions ship a plain object on `config.headers`; normalize.
    const headers =
      config.headers instanceof AxiosHeaders
        ? config.headers
        : new AxiosHeaders(config.headers as Record<string, string> | undefined);
    config.headers = headers;

    if (isEmbedded()) {
      headers.set('X-Requested-With', 'moses-iframe');
    }
    if (csrfToken && MUTATING_METHODS.has((config.method ?? 'get').toLowerCase())) {
      headers.set('X-CSRF-Token', csrfToken);
    }
    return config;
  });

  instance.interceptors.response.use(
    (response: AxiosResponse) => {
      captureCsrf(response.headers);
      return response;
    },
    (error: AxiosError) => {
      const response = error.response;
      if (response) {
        captureCsrf(response.headers);
        // Self-bootstrap: the first cookie-authed mutating call has no token
        // and 403s; that 403 carries X-Csrf-Token. Retry it exactly once.
        const cfg = error.config as RetriableConfig | undefined;
        const body = response.data as { error?: string; code?: string } | undefined;
        const isCsrf403 =
          response.status === 403 && /csrf/i.test(`${body?.error ?? ''}${body?.code ?? ''}`);
        if (isCsrf403 && csrfToken && cfg && !cfg.__csrfRetried) {
          cfg.__csrfRetried = true;
          return instance.request(cfg);
        }
      }
      return Promise.reject(toApiError(error));
    },
  );

  return instance;
}

/** Singleton client used by the rest of the app. */
export const api: AxiosInstance = attachInterceptors(
  axios.create({
    baseURL: resolveBaseURL(),
    withCredentials: true,
    headers: { 'Content-Type': 'application/json' },
  }),
);

export default api;
