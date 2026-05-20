// Renders the 6-digit linking code at large size with copy-to-clipboard.
//
// - Each digit is wrapped in a tile so the spacing stays readable; the full
//   numeric code is also exposed in a visually-hidden aria-label for
//   screen readers ("Linking code 1 2 3 4 5 6").
// - Copy button uses navigator.clipboard.writeText when available and falls
//   back to a no-op (with a status message) otherwise.

import { useState, type ReactElement } from 'react';

interface CodeDisplayProps {
  code: string;
}

export function CodeDisplay({ code }: CodeDisplayProps): ReactElement {
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);
  const digits = code.split('');

  async function onCopy(): Promise<void> {
    setCopyError(null);
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(code);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 2000);
      } else {
        setCopyError('Clipboard unavailable — copy the code manually.');
      }
    } catch {
      setCopyError('Could not copy the code — copy it manually.');
    }
  }

  // Spaced read-out for screen readers (e.g. "1 2 3 4 5 6") so the user
  // hears each digit instead of "one hundred twenty three thousand…".
  const readout = digits.join(' ');

  return (
    <div className="flex flex-col items-center gap-4">
      <p
        className="flex flex-wrap items-center justify-center gap-2"
        aria-label={`Linking code ${readout}`}
        role="text"
      >
        {digits.map((digit, idx) => (
          <span
            key={idx}
            aria-hidden="true"
            className="inline-flex h-14 w-10 items-center justify-center rounded-bento border border-moses-border bg-moses-surface-raised font-mono text-3xl font-semibold tabular-nums text-moses-text shadow-bento sm:h-16 sm:w-12 sm:text-4xl dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
          >
            {digit}
          </span>
        ))}
      </p>
      <button
        type="button"
        onClick={onCopy}
        aria-label={copied ? 'Code copied to clipboard' : 'Copy code to clipboard'}
        className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-4 text-sm font-medium text-moses-text hover:bg-moses-surface-sunken focus:outline-none focus:ring-2 focus:ring-moses-accent/40 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse"
      >
        {copied ? 'Copied!' : 'Copy code'}
      </button>
      {copyError && (
        <p role="alert" className="text-xs text-moses-status-error">
          {copyError}
        </p>
      )}
    </div>
  );
}

export default CodeDisplay;
