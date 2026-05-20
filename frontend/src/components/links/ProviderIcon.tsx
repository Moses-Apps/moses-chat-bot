// Small inline SVG icons for the chat providers supported (and previewed).
//
// Inline SVG keeps the bundle small and lets the icons inherit currentColor.
// Each icon is decorative — the surrounding context labels the provider.

import type { ReactElement } from 'react';

interface ProviderIconProps {
  provider: string;
  className?: string;
}

function TelegramIcon({ className }: { className?: string }): ReactElement {
  return (
    <svg
      viewBox="0 0 24 24"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      <path
        fill="currentColor"
        d="M9.84 16.13 9.5 20.4c.5 0 .72-.22.99-.47l2.38-2.27 4.93 3.6c.9.5 1.56.24 1.79-.83l3.24-15.15c.31-1.32-.48-1.85-1.36-1.52L2.3 9.86c-1.3.5-1.28 1.22-.22 1.55l5.04 1.57 11.7-7.36c.55-.36 1.05-.16.64.2"
      />
    </svg>
  );
}

function ChatBubbleIcon({ className }: { className?: string }): ReactElement {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden="true" focusable="false">
      <path
        fill="currentColor"
        d="M4 4h16a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H8l-4 4V6a2 2 0 0 1 2-2"
      />
    </svg>
  );
}

export function ProviderIcon({ provider, className }: ProviderIconProps): ReactElement {
  const cls = className ?? 'h-5 w-5';
  if (provider === 'telegram') return <TelegramIcon className={cls} />;
  return <ChatBubbleIcon className={cls} />;
}

export default ProviderIcon;
