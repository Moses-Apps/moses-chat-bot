// Moses iframe SDK ambient typings.
//
// The SDK is fetched at boot from /api/v1/sdk/iframe-sdk.js (served by
// moses-backend). It installs window.moses.actions.invoke which POSTs to
// /__moses/invoke on this app's own backend. The proxy forwards pod-to-pod
// to moses-backend with the user's JWT preserved.

export {};

declare global {
  interface MosesInvokeResult {
    result?: Record<string, unknown>;
    invocationId?: string;
    status?: string;
  }

  interface MosesInvokeError extends Error {
    status?: number;
    code?: string;
    hint?: string;
    retryAfterSeconds?: number;
  }

  interface Window {
    moses?: {
      __iframeSDKVersion?: string;
      actions?: {
        invoke?: (
          actionId: string,
          variables?: Record<string, unknown>,
        ) => Promise<MosesInvokeResult>;
      };
    };
  }
}
