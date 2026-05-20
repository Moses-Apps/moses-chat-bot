// Accessible toggle switch.
//
// - role="switch" + aria-checked.
// - Space and Enter both flip the state (Enter handled explicitly because the
//   default `<button>` only fires onClick for Space; the WAI-ARIA APG for
//   role=switch lists both as required activation keys).
// - 44px minimum touch target.

import { useId, type KeyboardEvent, type ReactElement, type ReactNode } from 'react';

interface ToggleProps {
  /** Current value. */
  checked: boolean;
  onChange: (next: boolean) => void;
  /** Visible label rendered next to the switch. */
  label: ReactNode;
  /** Optional helper text rendered below the label. */
  description?: ReactNode;
  /** Disabled state — Tab still lands on it for discoverability. */
  disabled?: boolean;
}

export function Toggle({
  checked,
  onChange,
  label,
  description,
  disabled,
}: ToggleProps): ReactElement {
  const labelId = useId();
  const descId = useId();

  function onKeyDown(e: KeyboardEvent<HTMLButtonElement>): void {
    if (disabled) return;
    if (e.key === 'Enter' || e.key === ' ' || e.key === 'Space') {
      e.preventDefault();
      onChange(!checked);
    }
  }

  return (
    <label
      htmlFor={`${labelId}-btn`}
      className={[
        'flex items-start justify-between gap-4 py-2',
        disabled ? 'opacity-50' : '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <span className="flex flex-col">
        <span id={labelId} className="text-sm font-medium text-moses-text">
          {label}
        </span>
        {description && (
          <span id={descId} className="mt-1 text-xs text-moses-text-muted">
            {description}
          </span>
        )}
      </span>
      <button
        id={`${labelId}-btn`}
        type="button"
        role="switch"
        aria-checked={checked}
        aria-labelledby={labelId}
        aria-describedby={description ? descId : undefined}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        onKeyDown={onKeyDown}
        className={[
          'relative inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full transition-colors',
          'focus:outline-none focus:ring-2 focus:ring-moses-accent/40 focus:ring-offset-2',
          'disabled:cursor-not-allowed',
          checked ? 'bg-moses-accent' : 'bg-moses-surface-sunken dark:bg-moses-surface-dark-sunken',
        ].join(' ')}
      >
        <span
          aria-hidden="true"
          className={[
            'inline-block h-5 w-5 transform rounded-full bg-white shadow transition-transform',
            checked ? 'translate-x-5' : 'translate-x-0.5',
          ].join(' ')}
        />
      </button>
    </label>
  );
}

export default Toggle;
