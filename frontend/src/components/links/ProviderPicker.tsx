// Radio-style provider picker for the LinkNew flow.
//
// v1: Telegram is the only enabled provider. Discord/Slack/WhatsApp are
// rendered as disabled "Coming soon" pills so users can see the roadmap.
//
// Implemented as a radiogroup; arrow keys move focus + selection.

import type { ReactElement } from 'react';
import ProviderIcon from './ProviderIcon';

export interface ProviderOption {
  id: string;
  label: string;
  description: string;
  enabled: boolean;
}

export const PROVIDERS: ProviderOption[] = [
  {
    id: 'telegram',
    label: 'Telegram',
    description: 'Chat with your Moses agents from anywhere.',
    enabled: true,
  },
  {
    id: 'discord',
    label: 'Discord',
    description: 'Coming soon.',
    enabled: false,
  },
  {
    id: 'slack',
    label: 'Slack',
    description: 'Coming soon.',
    enabled: false,
  },
  {
    id: 'whatsapp',
    label: 'WhatsApp',
    description: 'Coming soon.',
    enabled: false,
  },
];

interface ProviderPickerProps {
  value: string;
  onChange: (id: string) => void;
}

export function ProviderPicker({ value, onChange }: ProviderPickerProps): ReactElement {
  return (
    <div
      role="radiogroup"
      aria-label="Choose a chat provider"
      className="grid grid-cols-1 gap-3 sm:grid-cols-2"
    >
      {PROVIDERS.map((p) => {
        const selected = p.id === value;
        return (
          <label
            key={p.id}
            className={[
              'flex min-h-[64px] cursor-pointer items-start gap-3 rounded-bento border p-4 transition-colors',
              p.enabled
                ? selected
                  ? 'border-moses-accent bg-moses-accent-soft/40'
                  : 'border-moses-border hover:border-moses-accent/60 dark:border-moses-border-dark'
                : 'cursor-not-allowed border-dashed border-moses-border bg-moses-surface-sunken/40 opacity-60 dark:border-moses-border-dark dark:bg-moses-surface-dark-sunken/40',
            ].join(' ')}
          >
            <input
              type="radio"
              name="provider"
              value={p.id}
              checked={selected}
              onChange={() => p.enabled && onChange(p.id)}
              disabled={!p.enabled}
              aria-describedby={`provider-${p.id}-desc`}
              className="mt-1 h-4 w-4 accent-moses-accent"
            />
            <span className="flex items-start gap-3">
              <ProviderIcon
                provider={p.id}
                className="mt-0.5 h-5 w-5 text-moses-accent"
              />
              <span className="flex flex-col gap-1">
                <span className="flex items-center gap-2 text-sm font-medium text-moses-text">
                  {p.label}
                  {!p.enabled && (
                    <span className="rounded-full bg-moses-status-pending/15 px-2 py-0.5 text-xs font-medium text-moses-status-pending">
                      Coming soon
                    </span>
                  )}
                </span>
                <span
                  id={`provider-${p.id}-desc`}
                  className="text-xs text-moses-text-muted"
                >
                  {p.description}
                </span>
              </span>
            </span>
          </label>
        );
      })}
    </div>
  );
}

export default ProviderPicker;
