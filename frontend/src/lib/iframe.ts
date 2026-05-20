// iframe SDK helpers.
//
// The Moses iframe SDK (loaded from /api/v1/sdk/iframe-sdk.js — see index.html)
// installs window.moses.actions.invoke which round-trips to moses-backend via
// the app's own backend proxy. v1 of the platform supports only the
// `chat_prompt` and `launch_agent` action IDs (see SPEC.md §4).
//
// T-FE-1 does not call invoke() directly — the linking flow uses direct
// cookie-auth'd HTTP from inside the iframe (SPEC.md §4 step 3). This helper
// exists so later epics (T-FE-2/3) can reuse a single source of truth.

/** True when this document is loaded inside an iframe. */
export function isEmbedded(): boolean {
  // Same-origin sniff. Cross-origin access throws, which is also a "yes" signal.
  try {
    return window.self !== window.top;
  } catch {
    return true;
  }
}

/**
 * Invoke a platform action through the iframe SDK.
 *
 * Throws synchronously if the SDK is not loaded; otherwise returns the SDK's
 * promise unchanged. Callers are responsible for narrowing `actionId` to the
 * IDs the platform supports today.
 */
export async function invokePlatformAction(
  actionId: string,
  vars?: Record<string, unknown>,
): Promise<MosesInvokeResult> {
  const invoke = window.moses?.actions?.invoke;
  if (typeof invoke !== 'function') {
    throw new Error('Moses iframe SDK not loaded (window.moses.actions.invoke missing)');
  }
  return invoke(actionId, vars);
}
