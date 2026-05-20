// Modal confirmation dialog.
//
// - role="dialog" + aria-modal="true" + labelled by title + described by body.
// - Focus trap: Tab/Shift+Tab cycle within the dialog; Escape cancels.
// - On open: stash the previously-focused element and restore on close.
// - Backdrop click cancels.

import {
  useEffect,
  useId,
  useRef,
  type ReactElement,
  type ReactNode,
  type KeyboardEvent,
  type MouseEvent,
} from 'react';

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  /** Body copy; can be a string or arbitrary nodes. */
  description: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  /** Render the confirm button in a destructive style. */
  destructive?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

export function ConfirmDialog({
  open,
  title,
  description,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  destructive = false,
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps): ReactElement | null {
  const dialogRef = useRef<HTMLDivElement>(null);
  const previouslyFocused = useRef<HTMLElement | null>(null);
  const baseId = useId();
  const titleId = `${baseId}-title`;
  const descId = `${baseId}-desc`;

  useEffect(() => {
    if (!open) return;
    previouslyFocused.current = document.activeElement as HTMLElement | null;
    // Defer focus to the confirm button so the destructive default isn't pre-armed
    // by Enter; tabbing forward lands on it explicitly.
    const handle = window.setTimeout(() => {
      const root = dialogRef.current;
      if (!root) return;
      const focusables = root.querySelectorAll<HTMLElement>(FOCUSABLE);
      // Prefer cancel as initial focus to keep destructive flows safe.
      const cancelBtn = root.querySelector<HTMLElement>('[data-dialog-cancel]');
      (cancelBtn ?? focusables[0])?.focus();
    }, 0);
    return () => {
      window.clearTimeout(handle);
      previouslyFocused.current?.focus?.();
    };
  }, [open]);

  if (!open) return null;

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>): void {
    if (e.key === 'Escape') {
      e.stopPropagation();
      onCancel();
      return;
    }
    if (e.key !== 'Tab') return;
    const root = dialogRef.current;
    if (!root) return;
    const focusables = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
      (el) => !el.hasAttribute('disabled'),
    );
    if (focusables.length === 0) return;
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement as HTMLElement | null;
    if (e.shiftKey && active === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }

  function onBackdropClick(e: MouseEvent<HTMLDivElement>): void {
    if (e.target === e.currentTarget) onCancel();
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4"
      onClick={onBackdropClick}
      data-testid="confirm-dialog-backdrop"
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descId}
        onKeyDown={onKeyDown}
        className="w-full max-w-md rounded-bento border border-moses-border bg-moses-surface-raised p-6 shadow-bento-hover dark:border-moses-border-dark dark:bg-moses-surface-dark-raised"
      >
        <h2 id={titleId} className="text-base font-semibold tracking-tight">
          {title}
        </h2>
        <div id={descId} className="mt-2 text-sm text-moses-text-muted">
          {description}
        </div>
        <div className="mt-6 flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <button
            type="button"
            data-dialog-cancel
            onClick={onCancel}
            disabled={busy}
            className="min-h-[44px] rounded-bento border border-moses-border bg-moses-surface px-4 text-sm font-medium text-moses-text hover:bg-moses-surface-sunken focus:outline-none focus:ring-2 focus:ring-moses-accent/40 disabled:opacity-50 dark:border-moses-border-dark dark:bg-moses-surface-dark-sunken dark:text-moses-text-inverse"
          >
            {cancelLabel}
          </button>
          <button
            type="button"
            data-dialog-confirm
            onClick={onConfirm}
            disabled={busy}
            className={[
              'min-h-[44px] rounded-bento px-4 text-sm font-semibold text-white focus:outline-none focus:ring-2 focus:ring-moses-accent/40 disabled:opacity-50',
              destructive
                ? 'bg-moses-status-error hover:bg-moses-status-error/90'
                : 'bg-moses-accent hover:bg-moses-accent-hover',
            ].join(' ')}
          >
            {busy ? 'Working…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

export default ConfirmDialog;
