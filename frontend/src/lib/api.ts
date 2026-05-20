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

/**
 * Apply the moses-chat-bot interceptors to any axios instance. Exported for
 * testing; production code uses the pre-configured `api` singleton below.
 */
export function attachInterceptors(instance: AxiosInstance): AxiosInstance {
  instance.interceptors.request.use((config: InternalAxiosRequestConfig) => {
    if (isEmbedded()) {
      // Some axios versions ship a plain object on `config.headers`; normalize.
      if (config.headers instanceof AxiosHeaders) {
        config.headers.set('X-Requested-With', 'moses-iframe');
      } else {
        const next = new AxiosHeaders(config.headers as Record<string, string> | undefined);
        next.set('X-Requested-With', 'moses-iframe');
        config.headers = next;
      }
    }
    return config;
  });

  instance.interceptors.response.use(
    (response: AxiosResponse) => response,
    (error: AxiosError) => {
      const status = error.response?.status ?? 0;
      const data = error.response?.data as
        | { code?: string; error?: string; message?: string }
        | undefined;
      const apiError: ApiError = {
        status,
        code: data?.code ?? (status ? `http_${status}` : 'network_error'),
        message: data?.message ?? data?.error ?? error.message ?? 'Request failed',
        cause: error,
      };
      return Promise.reject(apiError);
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
