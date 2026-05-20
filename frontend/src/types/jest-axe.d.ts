// Ambient typings for jest-axe. The package ships JS only; we only use
// `axe()` + `toHaveNoViolations`. Keeping this file as a global declaration
// block (no top-level `import`/`export`) so `declare module 'jest-axe'`
// truly augments the global module map.

declare module 'jest-axe' {
  export interface AxeViolationNode {
    html: string;
    target: string[];
  }
  export interface AxeViolation {
    id: string;
    description: string;
    help: string;
    helpUrl: string;
    nodes: AxeViolationNode[];
  }
  export interface AxeResults {
    violations: AxeViolation[];
    passes: unknown[];
    incomplete: unknown[];
    inapplicable: unknown[];
  }
  export function axe(
    container: Element | Document,
    options?: Record<string, unknown>,
  ): Promise<AxeResults>;
  export const toHaveNoViolations: {
    toHaveNoViolations: (results: AxeResults) => {
      pass: boolean;
      message: () => string;
    };
  };
}

declare module 'vitest' {
  interface Assertion<T = unknown> {
    toHaveNoViolations(): T;
  }
  interface AsymmetricMatchersContaining {
    toHaveNoViolations(): void;
  }
}
