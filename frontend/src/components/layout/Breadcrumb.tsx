// Derives a breadcrumb from the current router location.
//
// Friendly labels are hardcoded for the routes T-FE-1 introduces; unknown
// segments are humanized (`:id` → "Detail").

import type { ReactElement } from 'react';
import { Link, useLocation, useParams } from 'react-router-dom';

const FRIENDLY: Record<string, string> = {
  '': 'Dashboard',
  link: 'Link',
  new: 'New',
  links: 'Links',
  messages: 'Messages',
  settings: 'Settings',
};

interface Crumb {
  label: string;
  to?: string;
}

function humanize(segment: string): string {
  if (FRIENDLY[segment] !== undefined) return FRIENDLY[segment];
  // For UUIDs / opaque ids we render a compact form (last 6 chars) so the
  // breadcrumb stays readable on narrow screens.
  if (segment.length > 8) return `…${segment.slice(-6)}`;
  return segment.charAt(0).toUpperCase() + segment.slice(1);
}

export function Breadcrumb(): ReactElement {
  const location = useLocation();
  const params = useParams();
  const segments = location.pathname.split('/').filter(Boolean);

  const crumbs: Crumb[] = [{ label: 'Dashboard', to: '/' }];
  let acc = '';
  segments.forEach((segment, idx) => {
    acc += `/${segment}`;
    const isLast = idx === segments.length - 1;
    // Skip duplicating the dashboard root.
    if (acc === '/') return;
    crumbs.push({
      label: params.id === segment ? humanize(segment) : humanize(segment),
      to: isLast ? undefined : acc,
    });
  });

  return (
    <nav aria-label="Breadcrumb" className="text-sm">
      <ol className="flex items-center gap-2">
        {crumbs.map((crumb, idx) => {
          const isLast = idx === crumbs.length - 1;
          return (
            <li key={`${crumb.label}-${idx}`} className="flex items-center gap-2">
              {idx > 0 && (
                <span aria-hidden="true" className="text-moses-text-subtle">
                  /
                </span>
              )}
              {crumb.to && !isLast ? (
                <Link
                  to={crumb.to}
                  className="text-moses-text-muted hover:text-moses-accent"
                >
                  {crumb.label}
                </Link>
              ) : (
                <span
                  aria-current={isLast ? 'page' : undefined}
                  className={isLast ? 'font-medium text-moses-text' : 'text-moses-text-muted'}
                >
                  {crumb.label}
                </span>
              )}
            </li>
          );
        })}
      </ol>
    </nav>
  );
}

export default Breadcrumb;
