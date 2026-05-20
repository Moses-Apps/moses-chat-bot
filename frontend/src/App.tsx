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

// The React Router basename must be the RUNTIME deploy prefix, not the
// build-time vite base. Vite is built with base './' so assets stay
// prefix-relative, which makes import.meta.env.BASE_URL './' — not a valid
// router basename. The nginx entrypoint injects the real MOSES_BASE_PATH
// into <meta name="moses-base-path">; read that. Falls back to '/' for
// standalone / jsdom where the tag is absent.
function resolveBaseName(): string {
  const content = document
    .querySelector('meta[name="moses-base-path"]')
    ?.getAttribute('content')
    ?.trim();
  if (content && content.startsWith('/')) {
    return content.replace(/\/+$/, '') || '/';
  }
  return '/';
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
