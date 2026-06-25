// ConnectTelegram — tenant-admin "Connect Telegram" wizard (moses-chat-bot-qcq).
//
// Telegram bots can ONLY be created by a human via @BotFather — there is no
// API or OAuth path. This wizard therefore does NOT emulate BotFather; it hands
// the admin off via a https://t.me/botfather deep link, gives copy-paste-ready
// commands, then takes the resulting token back through a paste field and
// POSTs it to the backend.
//
// The backend validates the token, encrypts it at rest, and immediately starts
// receiving messages by long-polling Telegram (moses-chat-bot-9so) — a purely
// outbound model that needs no webhook, no public URL, and no tunnel. The
// honest displayed flow is therefore just two steps: create the bot with
// BotFather, then paste the token. There is nothing else for the admin to do.
//
// The route is admin-gated: a non-admin viewer sees a "permission required"
// card instead of the wizard.

import { useCallback, useState, type ReactElement } from 'react';
import { Link as RouterLink } from 'react-router-dom';

import BentoCard from '@/components/layout/BentoCard';
import {
  useViewer,
  useTelegramInfo,
  useConnectTelegram,
  useDisconnectTelegram,
} from '@/api/hooks';
import { getErrorMessage } from '@/lib/errors';

const BOTFATHER_URL = 'https://t.me/botfather';

// The exact messages an admin sends to BotFather, in order. /newbot starts the
// flow; BotFather then prompts for a display name and a username (which must
// end in "bot").
const BOTFATHER_STEPS = [
  { label: 'Send this command to start a new bot', value: '/newbot' },
  {
    label: 'When asked for a name, reply with a display name',
    value: 'Moses Assistant',
  },
  {
    label: 'When asked for a username, reply with one ending in "bot"',
    value: 'moses_yourworkspace_bot',
  },
];

type Gate = 'checking' | 'allowed' | 'forbidden';

/** Small copy-to-clipboard button reused for each BotFather command. */
function CopyField({ value, label }: { value: string; label: string }): ReactElement {
  const [copied, setCopied] = useState(false);

  const onCopy = useCallback(async () => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(value);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 2000);
      }
    } catch {
      /* best-effort; the value is visible for manual copy */
    }
  }, [value]);

  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs text-moses-text-muted">{label}</span>
      <div className="flex items-center gap-2">
        <code className="flex-1 truncate rounded-bento border border-moses-border bg-moses-surface px-3 py-2 font-mono text-sm dark:border-moses-border-dark dark:bg-moses-surface-dark">
          {value}
        </code>
        <button
          type="button"
          onClick={() => void onCopy()}
          className="min-h-[44px] shrink-0 rounded-bento border border-moses-border bg-moses-surface-raised px-3 text-sm font-medium text-moses-text hover:bg-moses-surface-sunken focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  );
}

export default function ConnectTelegram(): ReactElement {
  const [token, setToken] = useState('');
  const [error, setError] = useState<string | null>(null);

  // Admin gate (server state). Fail closed: a viewer error → forbidden.
  const viewer = useViewer();
  const isAdmin = viewer.data?.isTenantAdmin ?? false;

  // Only an admin loads the connection state — mirrors the original flow where
  // getTelegramInfo is never called for a non-admin viewer.
  const telegram = useTelegramInfo(isAdmin);
  const info = telegram.data ?? null;

  const connect = useConnectTelegram();
  const disconnect = useDisconnectTelegram();
  const submitting = connect.isPending;
  const disconnecting = disconnect.isPending;

  const gate: Gate = viewer.isPending
    ? 'checking'
    : isAdmin
      ? 'allowed'
      : 'forbidden';

  // Surface a viewer-resolution failure on the forbidden card.
  const viewerError = viewer.isError ? getErrorMessage(viewer.error) : null;
  const displayError = error ?? viewerError;

  const onConnect = useCallback(() => {
    const trimmed = token.trim();
    if (!trimmed) {
      setError('Paste the token BotFather gave you.');
      return;
    }
    setError(null);
    connect.mutate(trimmed, {
      onSuccess: () => setToken(''),
      onError: (err) =>
        setError(getErrorMessage(err) || 'Could not connect the bot.'),
    });
  }, [token, connect]);

  const onDisconnect = useCallback(() => {
    setError(null);
    disconnect.mutate(undefined, {
      onError: (err) =>
        setError(getErrorMessage(err) || 'Could not disconnect the bot.'),
    });
  }, [disconnect]);

  if (gate === 'checking') {
    return (
      <div className="grid grid-cols-1 gap-4">
        <BentoCard title="Connect Telegram">
          <p className="text-sm text-moses-text-muted" aria-live="polite">
            Checking your permissions…
          </p>
        </BentoCard>
      </div>
    );
  }

  if (gate === 'forbidden') {
    return (
      <div className="grid grid-cols-1 gap-4">
        <BentoCard
          title="Tenant admin required"
          subtitle="Only administrators can connect a Telegram bot"
        >
          <p className="text-sm text-moses-text-muted">
            Connecting a Telegram bot for the whole workspace is an
            administrator action. Ask a tenant admin to set this up.
          </p>
          {displayError && (
            <p role="alert" className="mt-3 text-sm text-moses-status-error">
              {displayError}
            </p>
          )}
          <RouterLink
            to="/"
            className="mt-4 inline-flex min-h-[44px] items-center rounded-bento border border-moses-border bg-moses-surface-raised px-4 text-sm font-medium text-moses-text hover:bg-moses-surface-sunken focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
          >
            Back to dashboard
          </RouterLink>
        </BentoCard>
      </div>
    );
  }

  // gate === 'allowed' — render the wizard or the connected state.
  if (info?.configured) {
    return (
      <div className="grid grid-cols-1 gap-4">
        <BentoCard
          title="Telegram bot connected"
          subtitle="Your workspace can now link Telegram chats"
        >
          <div className="flex flex-col gap-4">
            <div className="flex items-center gap-3 rounded-bento border border-moses-status-active/40 bg-moses-status-active/10 p-4">
              <span
                aria-hidden="true"
                className="inline-flex h-10 w-10 items-center justify-center rounded-full bg-moses-status-active/15 text-moses-status-active"
              >
                <svg viewBox="0 0 24 24" className="h-6 w-6">
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
              <div>
                <p className="font-semibold">Connected</p>
                <p className="text-sm text-moses-text-muted">
                  Bot handle:{' '}
                  <span className="font-mono">
                    {info.username ? `@${info.username}` : 'unknown'}
                  </span>
                </p>
              </div>
            </div>
            <p className="text-sm text-moses-text-muted">
              Workspace members can now link their Telegram chat from{' '}
              <RouterLink to="/link/new" className="text-moses-accent hover:underline">
                Link a chat
              </RouterLink>
              . The bot is already receiving messages — no webhook, no public
              URL, and no redeploy needed.
            </p>
            {error && (
              <p role="alert" className="text-sm text-moses-status-error">
                {error}
              </p>
            )}
            <button
              type="button"
              onClick={() => void onDisconnect()}
              disabled={disconnecting}
              className="self-start min-h-[44px] rounded-bento border border-moses-status-error/50 bg-moses-surface-raised px-4 text-sm font-medium text-moses-status-error hover:bg-moses-status-error/10 focus:outline-none focus:ring-2 focus:ring-moses-status-error/40 disabled:opacity-50 dark:bg-moses-surface-dark-raised"
            >
              {disconnecting ? 'Disconnecting…' : 'Disconnect bot'}
            </button>
          </div>
        </BentoCard>
      </div>
    );
  }

  return (
    <div className="grid grid-cols-1 gap-4">
      <BentoCard
        title="Create a bot with BotFather"
        subtitle="Step 1 of 2 — Telegram requires a human to create the bot"
      >
        <div className="flex flex-col gap-4">
          <p className="text-sm text-moses-text-muted">
            Telegram bots can only be created in the Telegram app through its
            official @BotFather account. Open BotFather and send these three
            messages in order — BotFather will reply with a token at the end.
          </p>
          <a
            href={BOTFATHER_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex min-h-[44px] w-fit items-center rounded-bento bg-moses-accent px-4 text-sm font-semibold text-white hover:bg-moses-accent-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40"
          >
            Open @BotFather in Telegram
          </a>
          <ol className="flex flex-col gap-3">
            {BOTFATHER_STEPS.map((s, i) => (
              <li key={s.value} className="flex flex-col gap-1">
                <CopyField label={`${i + 1}. ${s.label}`} value={s.value} />
              </li>
            ))}
          </ol>
          <p className="text-xs text-moses-text-subtle">
            The display name and username above are examples — pick your own.
            The username must be unique and end in "bot".
          </p>
        </div>
      </BentoCard>

      <BentoCard
        title="Paste the bot token"
        subtitle="Step 2 of 2 — BotFather sends a token like 123456789:ABC…"
      >
        <div className="flex flex-col gap-4">
          <label htmlFor="telegram-token" className="flex flex-col gap-1">
            <span className="text-sm font-medium">Bot token from BotFather</span>
            <input
              id="telegram-token"
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder="123456789:AAExampleTokenFromBotFather"
              className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface px-3 font-mono text-sm text-moses-text focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark dark:text-moses-text-inverse"
            />
          </label>
          {error && (
            <p role="alert" className="text-sm text-moses-status-error">
              {error}
            </p>
          )}
          <button
            type="button"
            onClick={() => void onConnect()}
            disabled={submitting || token.trim() === ''}
            className="self-start min-h-[44px] rounded-bento bg-moses-accent px-4 text-sm font-semibold text-white hover:bg-moses-accent-hover focus:outline-none focus:ring-2 focus:ring-moses-accent/40 disabled:opacity-50"
          >
            {submitting ? 'Connecting…' : 'Connect bot'}
          </button>
        </div>
      </BentoCard>
    </div>
  );
}
