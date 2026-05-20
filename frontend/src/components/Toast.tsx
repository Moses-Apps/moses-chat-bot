// Global toast portal.
//
// - Single in-flight toast for v1 (queueing is out of scope per T-FE-3).
// - role="status" + aria-live="polite" so screen readers announce on idle.
// - Auto-dismisses after `durationMs` (default 2500ms). Clicking dismisses
//   immediately.
//
// Use via:
//
//   const { show } = useToast();
//   show('Saved');
//
// The provider lives at the page level (each page that needs toasts wraps its
// content in <ToastProvider>); the global app shell intentionally does NOT
// mount one, so unrelated pages can't accidentally surface stale toasts.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactElement,
  type ReactNode,
} from 'react';

interface ToastApi {
  show: (message: string, options?: { tone?: 'success' | 'error' }) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

interface ToastProviderProps {
  children: ReactNode;
  /** Auto-dismiss delay in ms. */
  durationMs?: number;
}

interface ToastState {
  id: number;
  message: string;
  tone: 'success' | 'error';
}

export function ToastProvider({
  children,
  durationMs = 2500,
}: ToastProviderProps): ReactElement {
  const [toast, setToast] = useState<ToastState | null>(null);
  const timer = useRef<number | undefined>(undefined);
  const idRef = useRef(0);

  const dismiss = useCallback(() => {
    if (timer.current !== undefined) {
      window.clearTimeout(timer.current);
      timer.current = undefined;
    }
    setToast(null);
  }, []);

  const show = useCallback<ToastApi['show']>(
    (message, options) => {
      idRef.current += 1;
      const id = idRef.current;
      if (timer.current !== undefined) window.clearTimeout(timer.current);
      setToast({ id, message, tone: options?.tone ?? 'success' });
      timer.current = window.setTimeout(() => {
        // Only clear if this toast is still the active one.
        setToast((cur) => (cur && cur.id === id ? null : cur));
        timer.current = undefined;
      }, durationMs);
    },
    [durationMs],
  );

  useEffect(() => {
    return () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
    };
  }, []);

  return (
    <ToastContext.Provider value={{ show }}>
      {children}
      {/* Fixed top-right. role=status keeps it out of focus order. */}
      <div
        aria-live="polite"
        aria-atomic="true"
        className="pointer-events-none fixed right-4 top-4 z-50 flex flex-col items-end gap-2"
      >
        {toast && (
          <button
            type="button"
            onClick={dismiss}
            data-testid="toast"
            className={[
              'pointer-events-auto rounded-bento border px-4 py-2 text-sm font-medium shadow-bento-hover',
              toast.tone === 'error'
                ? 'border-moses-status-error/40 bg-moses-status-error/10 text-moses-status-error'
                : 'border-moses-status-active/40 bg-moses-status-active/10 text-moses-status-active',
            ].join(' ')}
          >
            {toast.message}
          </button>
        )}
      </div>
    </ToastContext.Provider>
  );
}

/**
 * Returns a stable `show()` function. Calls outside a ToastProvider become
 * no-ops so leaf components don't crash on isolated unit tests.
 */
export function useToast(): ToastApi {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    return { show: () => undefined };
  }
  return ctx;
}

export default ToastProvider;
