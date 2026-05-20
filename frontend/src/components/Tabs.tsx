// Minimal WAI-ARIA tabs primitive.
//
// - Roving-tabindex keyboard nav (ArrowLeft / ArrowRight / Home / End).
// - On narrow screens (<sm), the tab strip collapses to a native <select> so
//   the touch target is always large enough.
// - Controlled API: parent owns `value`, child fires `onChange(id)`.

import { useId, type ReactElement, type ReactNode, type KeyboardEvent } from 'react';

export interface TabItem {
  id: string;
  label: string;
  /** Optional danger styling for irreversible-action tabs. */
  tone?: 'default' | 'danger';
  content: ReactNode;
}

interface TabsProps {
  items: TabItem[];
  value: string;
  onChange: (id: string) => void;
  /** Accessible label for the tablist (e.g. "Link sections"). */
  ariaLabel: string;
}

export function Tabs({ items, value, onChange, ariaLabel }: TabsProps): ReactElement {
  const baseId = useId();
  const activeIndex = Math.max(
    0,
    items.findIndex((t) => t.id === value),
  );

  function onKeyDown(e: KeyboardEvent<HTMLButtonElement>): void {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return;
    e.preventDefault();
    let next = activeIndex;
    if (e.key === 'ArrowRight') next = (activeIndex + 1) % items.length;
    else if (e.key === 'ArrowLeft') next = (activeIndex - 1 + items.length) % items.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = items.length - 1;
    onChange(items[next].id);
    // Move focus to the newly-active tab so screen readers track it.
    const root = (e.currentTarget as HTMLElement).closest('[role="tablist"]');
    const btn = root?.querySelector<HTMLButtonElement>(
      `[data-tab-id="${items[next].id}"]`,
    );
    btn?.focus();
  }

  const activeItem = items[activeIndex];

  return (
    <div>
      {/* Mobile: <select> collapse */}
      <div className="sm:hidden mb-4">
        <label htmlFor={`${baseId}-select`} className="sr-only">
          {ariaLabel}
        </label>
        <select
          id={`${baseId}-select`}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="w-full min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-3 text-sm text-moses-text focus:border-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/30 dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
        >
          {items.map((item) => (
            <option key={item.id} value={item.id}>
              {item.label}
            </option>
          ))}
        </select>
      </div>

      {/* sm+: real tablist */}
      <div
        role="tablist"
        aria-label={ariaLabel}
        className="hidden sm:flex border-b border-moses-border dark:border-moses-border-dark"
      >
        {items.map((item) => {
          const isActive = item.id === value;
          const isDanger = item.tone === 'danger';
          return (
            <button
              key={item.id}
              type="button"
              role="tab"
              data-tab-id={item.id}
              id={`${baseId}-tab-${item.id}`}
              aria-controls={`${baseId}-panel-${item.id}`}
              aria-selected={isActive}
              tabIndex={isActive ? 0 : -1}
              onClick={() => onChange(item.id)}
              onKeyDown={onKeyDown}
              className={[
                'min-h-[44px] px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors',
                isActive
                  ? isDanger
                    ? 'border-moses-status-error text-moses-status-error'
                    : 'border-moses-accent text-moses-accent'
                  : 'border-transparent text-moses-text-muted hover:text-moses-text',
                isDanger && !isActive ? 'hover:text-moses-status-error' : '',
              ]
                .filter(Boolean)
                .join(' ')}
            >
              {item.label}
            </button>
          );
        })}
      </div>

      <div
        role="tabpanel"
        id={`${baseId}-panel-${activeItem.id}`}
        aria-labelledby={`${baseId}-tab-${activeItem.id}`}
        tabIndex={0}
        className="pt-4 focus:outline-none"
      >
        {activeItem.content}
      </div>
    </div>
  );
}

export default Tabs;
