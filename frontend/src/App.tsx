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
import ConnectTelegram from '@/pages/ConnectTelegram';
import NotFound from '@/pages/NotFound';
import { mosesBasePath } from '@/lib/basePath';

// The React Router basename is the RUNTIME deploy prefix (see lib/basePath),
// never the build-time vite base. '' (standalone / jsdom) maps to '/'.
function resolveBaseName(): string {
  return mosesBasePath() || '/';
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
          {/* Admin-gated in the page itself (ConnectTelegram checks /auth/me). */}
          <Route path="/settings/telegram" element={<ConnectTelegram />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </PageShell>
    </BrowserRouter>
  );
}
