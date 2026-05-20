// Playwright config for moses-chat-bot frontend E2E.
//
// We boot `vite preview` against the static built site so the test surface is
// deterministic (no HMR, no dev-only warnings). The backend is mocked at the
// network layer via `page.route()` — see `e2e/fixtures.ts`. Real moses-backend
// orchestration in CI is deferred (heavier; see SPEC §14).
//
// `webServer` reuses an already-running preview server locally; CI gets a
// fresh boot. Output lives under `e2e-results/` (gitignored).

import { defineConfig, devices } from '@playwright/test';

const PORT = 4173;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI
    ? [['github'], ['html', { outputFolder: 'e2e-results/html', open: 'never' }]]
    : [['list'], ['html', { outputFolder: 'e2e-results/html', open: 'never' }]],
  outputDir: 'e2e-results/artifacts',
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  // The production build uses `base: './'` (subpath-friendly for the iframe
  // mount under /apps/<tenant>/moses-chat-bot/). That bakes BASE_URL="./" into
  // the bundle, which makes BrowserRouter's basename invalid when served at
  // the URL root. We rebuild with `--base /` purely for E2E so the same code
  // mounts cleanly at `http://localhost:4173/`.
  webServer: {
    command: `npx vite build --base / && npx vite preview --base / --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
