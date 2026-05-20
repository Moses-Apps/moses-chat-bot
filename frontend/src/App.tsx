// Router shell. Page components live in src/pages/ as stubs for T-FE-1;
// real pages land in T-FE-2 (linking flow) and T-FE-3 (messages / settings).

import type { ReactElement } from 'react';
import { BrowserRouter, Route, Routes } from 'react-router-dom';

import PageShell from '@/components/layout/PageShell';
import Dashboard from '@/pages/Dashboard';
import LinkNew from '@/pages/LinkNew';
import LinkDetail from '@/pages/LinkDetail';
import Messages from '@/pages/Messages';
import Settings from '@/pages/Settings';
import NotFound from '@/pages/NotFound';

// import.meta.env.BASE_URL is the vite-injected base path. The fallback covers
// jsdom / SSR-ish environments where Vite's typings aren't in play.
function resolveBaseName(): string {
  const env = (import.meta as { env?: { BASE_URL?: string } }).env;
  return env?.BASE_URL ?? '/';
}

export default function App(): ReactElement {
  return (
    <BrowserRouter basename={resolveBaseName()}>
      <PageShell>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/link/new" element={<LinkNew />} />
          <Route path="/links/:id" element={<LinkDetail />} />
          <Route path="/messages" element={<Messages />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </PageShell>
    </BrowserRouter>
  );
}
