// Debounced search input primitive.
//
// - Controlled by the caller via `value` + `onChange`.
// - The underlying <input> keeps its own ephemeral text state so the user sees
//   each keystroke immediately; `onChange` only fires after the debounce
//   window settles. Switching `value` externally (e.g. a "Clear" button)
//   resets the ephemeral state.
// - Accessible: a real <label> via `aria-label` (visually-hidden by default),
//   focus ring, role="searchbox".

import {
  useEffect,
  useRef,
  useState,
  type ChangeEvent,
  type ReactElement,
} from 'react';

interface SearchInputProps {
  value: string;
  onChange: (value: string) => void;
  /** Visible-or-screen-reader-only label. Required for a11y. */
  ariaLabel: string;
  placeholder?: string;
  /** Debounce window, ms. Defaults to 250. */
  debounceMs?: number;
  className?: string;
  id?: string;
}

export function SearchInput({
  value,
  onChange,
  ariaLabel,
  placeholder,
  debounceMs = 250,
  className,
  id,
}: SearchInputProps): ReactElement {
  const [draft, setDraft] = useState(value);
  const timer = useRef<number | undefined>(undefined);
  // Track the last value we *emitted* so an external reset to '' doesn't loop.
  const lastEmitted = useRef(value);

  // External value changes (e.g. parent resets to '') overwrite the draft.
  useEffect(() => {
    if (value !== lastEmitted.current) {
      setDraft(value);
      lastEmitted.current = value;
    }
  }, [value]);

  // Clear pending debounce on unmount.
  useEffect(() => {
    return () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
    };
  }, []);

  function handleChange(e: ChangeEvent<HTMLInputElement>): void {
    const next = e.target.value;
    setDraft(next);
    if (timer.current !== undefined) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      lastEmitted.current = next;
      onChange(next);
    }, debounceMs);
  }

  return (
    <input
      id={id}
      type="search"
      role="searchbox"
      aria-label={ariaLabel}
      placeholder={placeholder}
      value={draft}
      onChange={handleChange}
      className={[
        'w-full min-h-[44px] rounded-bento border border-moses-border bg-moses-surface-raised px-3 text-sm text-moses-text',
        'placeholder:text-moses-text-subtle focus:border-moses-accent focus:outline-none focus:ring-2 focus:ring-moses-accent/30',
        'dark:border-moses-border-dark dark:bg-moses-surface-dark-raised dark:text-moses-text-inverse',
        className ?? '',
      ]
        .filter(Boolean)
        .join(' ')}
    />
  );
}

export default SearchInput;
