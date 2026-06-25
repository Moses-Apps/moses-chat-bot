// Normalize an unknown thrown/rejected value to a display message.
//
// TanStack Query surfaces whatever the queryFn rejected with. In this app the
// axios response interceptor (lib/api.ts) rejects with a structured ApiError
// (`{ status, code, message }`) rather than an Error instance, so callers must
// not assume `error instanceof Error`. Use this in components instead.
export function getErrorMessage(e: unknown): string {
  if (e == null) return 'Unexpected error';
  if (e instanceof Error) return e.message;
  if (typeof e === 'string') return e;
  if (typeof e === 'object' && 'message' in e) {
    const msg = (e as { message?: unknown }).message;
    if (typeof msg === 'string' && msg.length > 0) return msg;
  }
  return 'Unexpected error';
}
