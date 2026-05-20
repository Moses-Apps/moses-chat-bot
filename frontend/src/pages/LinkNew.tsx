// LinkNew — 4-step flow:
//   1. pick provider (Telegram only enabled in v1)
//   2. generate code → mint platform key → POST /links/codes
//   3. show code + countdown + poll until completion or expiry
//   4. success → redirect to /links/:id
//
// Security:
//   - The plaintext MCP key returned by createMcpKey() lives ONLY in this
//     component's closure (a useRef). It's wiped the moment createLinkCode()
//     resolves (step 2.4 in the brief). It never enters Zustand, never enters
//     localStorage, never appears in JSX.
//   - The platform key UUID (keyId) is retained so we can revoke on expiry /
//     cancel — that one is non-sensitive.

import { useCallback, useEffect, useRef, useState, type ReactElement } from 'react';
import { useNavigate } from 'react-router-dom';

import BentoCard from '@/components/layout/BentoCard';
import ProviderPicker from '@/components/links/ProviderPicker';
import CodeDisplay from '@/components/links/CodeDisplay';
import CountdownTimer from '@/components/links/CountdownTimer';
import { createMcpKey, revokeMcpKey } from '@/lib/platform';
import { createLinkCode, pollLinkCode } from '@/lib/bot-api';
import type { ApiError } from '@/lib/api';

const KEY_TTL_DAYS = 90;
const CODE_TTL_SECONDS = 60;
const POLL_INTERVAL_MS = 2000;

type Step = 'pick' | 'generating' | 'code' | 'expired' | 'success';

interface CodeState {
  code: string;
  expiresAt: string;
  keyId: string;
}

function toApiError(err: unknown): ApiError {
  if (err && typeof err === 'object' && 'status' in err && 'message' in err) {
    return err as ApiError;
  }
  return {
    status: 0,
    code: 'unknown',
    message: err instanceof Error ? err.message : 'Unknown error',
  };
}

function plus90DaysIso(): string {
  const d = new Date();
  d.setUTCDate(d.getUTCDate() + KEY_TTL_DAYS);
  return d.toISOString();
}

export default function LinkNew(): ReactElement {
  const navigate = useNavigate();
  const [provider, setProvider] = useState<string>('telegram');
  const [step, setStep] = useState<Step>('pick');
  const [codeState, setCodeState] = useState<CodeState | null>(null);
  const [linkId, setLinkId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // The plaintext MCP key briefly held between createMcpKey() and
  // createLinkCode(). Stored in a ref so it never enters React state (and
  // therefore never appears in any test-rendered DOM).
  const plaintextKeyRef = useRef<string | null>(null);

  // Track whether we have effectively "consumed" the key (handed it off) so
  // unmount cleanup doesn't try to revoke a key that's now tied to a link.
  const keyConsumedRef = useRef(false);

  const cancelGeneration = useCallback(async () => {
    plaintextKeyRef.current = null;
    const keyId = codeState?.keyId;
    setCodeState(null);
    setError(null);
    setStep('pick');
    if (keyId && !keyConsumedRef.current) {
      try {
        await revokeMcpKey(keyId);
      } catch {
        // Best-effort cleanup; the key will TTL out on its own.
      }
    }
    keyConsumedRef.current = false;
  }, [codeState]);

  const generateCode = useCallback(async () => {
    setError(null);
    setStep('generating');
    keyConsumedRef.current = false;
    let keyId = '';
    try {
      const key = await createMcpKey({
        name: `telegram:bot:${crypto.randomUUID()}`,
        profile: 'moses-manager-full',
        expiresAt: plus90DaysIso(),
      });
      keyId = key.keyId;
      plaintextKeyRef.current = key.key;

      const codeResult = await createLinkCode({
        apiKey: key.key,
        apiKeyIdHint: key.keyId,
        expiresInSeconds: CODE_TTL_SECONDS,
      });

      // The plaintext key has done its job: the bot has stored it encrypted.
      // Wipe it from the closure right now.
      plaintextKeyRef.current = null;

      setCodeState({ code: codeResult.code, expiresAt: codeResult.expiresAt, keyId });
      setStep('code');
    } catch (err) {
      const apiErr = toApiError(err);
      // If we already minted a key but didn't get a code, the user has an
      // orphan key on the platform side — revoke it (best-effort) so we don't
      // litter the user's key list.
      plaintextKeyRef.current = null;
      if (keyId) {
        try {
          await revokeMcpKey(keyId);
        } catch {
          /* best-effort */
        }
      }
      setError(apiErr.message || 'Could not generate a code. Try again.');
      setStep('pick');
    }
  }, []);

  // Poll loop while step === 'code'.
  useEffect(() => {
    if (step !== 'code' || !codeState) return;
    let cancelled = false;

    async function tick(): Promise<void> {
      try {
        const res = await pollLinkCode(codeState!.code);
        if (cancelled) return;
        if (res.status === 'completed' && res.linkId) {
          // Mark the key as consumed so unmount cleanup doesn't revoke it.
          keyConsumedRef.current = true;
          setLinkId(res.linkId);
          setStep('success');
          return;
        }
        // 'pending' → keep polling.
      } catch (err) {
        if (cancelled) return;
        const apiErr = toApiError(err);
        if (apiErr.status === 410) {
          // Server says the code is expired — revoke the platform key.
          if (codeState && !keyConsumedRef.current) {
            try {
              await revokeMcpKey(codeState.keyId);
              keyConsumedRef.current = true;
            } catch {
              /* best-effort */
            }
          }
          setStep('expired');
          return;
        }
        // 404 → keep polling until countdown hits zero.
        if (apiErr.status === 404) return;
        // Anything else → surface the error but keep the code visible so the
        // user can still claim it from Telegram.
        setError(apiErr.message);
      }
    }

    // Fire immediately so the user doesn't wait 2s for the first poll.
    void tick();
    const id = window.setInterval(() => void tick(), POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [step, codeState]);

  // Auto-redirect on success.
  useEffect(() => {
    if (step !== 'success' || !linkId) return;
    const id = window.setTimeout(() => navigate(`/links/${linkId}`), 2000);
    return () => window.clearTimeout(id);
  }, [step, linkId, navigate]);

  // On unmount: if we still hold an unconsumed key, revoke it so we don't
  // leak orphan keys when the user navigates away.
  useEffect(() => {
    return () => {
      const keyId = codeState?.keyId;
      plaintextKeyRef.current = null;
      if (keyId && !keyConsumedRef.current) {
        // Best-effort: wrap so a non-promise return (e.g. from a test mock)
        // doesn't blow up the cleanup phase.
        try {
          const p = revokeMcpKey(keyId) as Promise<void> | undefined;
          p?.catch?.(() => undefined);
        } catch {
          /* best-effort */
        }
      }
    };
  }, [codeState]);

  const onExpired = useCallback(async () => {
    if (!codeState) return;
    if (!keyConsumedRef.current) {
      try {
        await revokeMcpKey(codeState.keyId);
        keyConsumedRef.current = true;
      } catch {
        /* best-effort */
      }
    }
    setStep('expired');
  }, [codeState]);

  return (
    <div className="grid grid-cols-1 gap-4">
      {step === 'pick' && (
        <BentoCard title="Link a chat" subtitle="Step 1 of 3 — pick a provider">
          <ProviderPicker value={provider} onChange={setProvider} />
          {error && (
            <p role="alert" className="mt-4 text-sm text-moses-status-error">
              {error}
            </p>
          )}
          <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:justify-end">
            <button
              type="button"
              onClick={() => void generateCode()}
              disabled={provider !== 'telegram'}
              className="min-h-[44px] rounded-bento bg-moses-accent px-4 text-sm font-semibold text-white hover:bg-moses-accent-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40 disabled:opacity-50"
            >
              Generate code
            </button>
          </div>
        </BentoCard>
      )}

      {step === 'generating' && (
        <BentoCard title="Generating your code…">
          <p className="text-sm text-moses-text-muted" aria-live="polite">
            Minting your personal Moses key and asking the bot for a code.
          </p>
        </BentoCard>
      )}

      {step === 'code' && codeState && (
        <BentoCard
          title="Enter this code in Telegram"
          subtitle="Step 3 of 3 — claim within 60 seconds"
        >
          <div className="flex flex-col items-center gap-6 py-4">
            <CodeDisplay code={codeState.code} />
            <CountdownTimer expiresAt={codeState.expiresAt} onExpired={onExpired} />
            <div className="max-w-md rounded-bento border border-moses-border bg-moses-surface p-4 text-sm text-moses-text dark:border-moses-border-dark dark:bg-moses-surface-dark">
              <p className="font-medium">How to claim it</p>
              <ol className="mt-2 list-decimal space-y-1 pl-5 text-moses-text-muted">
                <li>Open Telegram on any device.</li>
                <li>
                  Find <span className="font-mono">@moses_&lt;tenant&gt;_bot</span>.
                </li>
                <li>
                  Send <span className="font-mono">/link {codeState.code}</span>.
                </li>
              </ol>
            </div>
            {error && (
              <p role="alert" className="text-sm text-moses-status-error">
                {error}
              </p>
            )}
            <button
              type="button"
              onClick={() => void cancelGeneration()}
              className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-4 text-sm font-medium text-moses-text hover:bg-moses-surface-sunken focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
            >
              Cancel
            </button>
          </div>
        </BentoCard>
      )}

      {step === 'expired' && (
        <BentoCard title="Code expired" subtitle="No worries — we cleaned up the key">
          <p className="text-sm text-moses-text-muted">
            The 60-second window closed before Telegram confirmed the link. The
            unused key has been revoked.
          </p>
          <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:justify-end">
            <button
              type="button"
              onClick={() => void generateCode()}
              className="min-h-[44px] rounded-bento bg-moses-accent px-4 text-sm font-semibold text-white hover:bg-moses-accent-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40"
            >
              Try again
            </button>
          </div>
        </BentoCard>
      )}

      {step === 'success' && (
        <BentoCard title="Linked!">
          <div
            className="flex flex-col items-center gap-3 py-6 text-center"
            aria-live="polite"
          >
            <span
              aria-hidden="true"
              className="inline-flex h-16 w-16 items-center justify-center rounded-full bg-moses-status-active/15 text-moses-status-active"
            >
              <svg viewBox="0 0 24 24" className="h-8 w-8">
                <path
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="3"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M5 12l5 5 9-11"
                />
              </svg>
            </span>
            <p className="text-base font-semibold">Telegram linked successfully</p>
            <p className="text-sm text-moses-text-muted">Redirecting…</p>
          </div>
        </BentoCard>
      )}
    </div>
  );
}
