// 404 page. Accessible link back to the dashboard.

import type { ReactElement } from 'react';
import { Link } from 'react-router-dom';

export default function NotFound(): ReactElement {
  return (
    <div role="alert" className="rounded-bento border border-moses-border bg-moses-surface-raised p-6 text-center dark:border-moses-border-dark dark:bg-moses-surface-dark-raised">
      <h1 className="text-xl font-semibold">Page not found</h1>
      <p className="mt-2 text-sm text-moses-text-muted">
        The route you tried to open does not exist.
      </p>
      <Link
        to="/"
        className="mt-4 inline-block rounded-md bg-moses-accent px-3 py-2 text-sm font-medium text-white hover:bg-moses-accent-hover focus:outline-none"
      >
        Back to dashboard
      </Link>
    </div>
  );
}
