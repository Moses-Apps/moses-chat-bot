// Vitest global setup. Pulls in jest-dom matchers and jest-axe extensions
// so individual test files don't have to repeat the wiring.
//
// `expect` is available globally because vite.config.ts sets `test.globals: true`
// and tsconfig "types" includes "vitest/globals".

import '@testing-library/jest-dom/vitest';
import { toHaveNoViolations } from 'jest-axe';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
(expect as any).extend(toHaveNoViolations);

// jsdom in this Node version doesn't expose localStorage by default
// (the runtime prints an ExperimentalWarning about --localstorage-file).
// Zustand's `persist` middleware reads window.localStorage at module init, so
// we install an in-memory shim before any user module loads. Tests are
// isolated per-file in vitest, so this state doesn't leak across files.
if (typeof globalThis.localStorage === 'undefined') {
  const memory = new Map<string, string>();
  const shim: Storage = {
    get length(): number {
      return memory.size;
    },
    clear: () => memory.clear(),
    getItem: (key) => (memory.has(key) ? memory.get(key)! : null),
    key: (i) => Array.from(memory.keys())[i] ?? null,
    removeItem: (key) => void memory.delete(key),
    setItem: (key, value) => void memory.set(key, String(value)),
  };
  Object.defineProperty(globalThis, 'localStorage', {
    value: shim,
    configurable: true,
    writable: true,
  });
  // jsdom's `window` is the same object as `globalThis`; defineProperty above
  // covers both, but make the property descriptor cooperative either way.
  if (typeof window !== 'undefined' && !('localStorage' in window)) {
    Object.defineProperty(window, 'localStorage', {
      value: shim,
      configurable: true,
    });
  }
}
