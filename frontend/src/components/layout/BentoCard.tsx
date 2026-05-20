// Generic card wrapper for Moses' Bento Grid layout.
//
// - 4px grid padding (p-4 / p-6 = 16px / 24px).
// - Rounded corners + subtle shadow via the `bento` design tokens.
// - Dark-mode aware through the moses.* surface palette.

import type { ReactNode, ReactElement } from 'react';

interface BentoCardProps {
  /** Visible card title; rendered as an <h2>. Omit for a chrome-less card. */
  title?: string;
  /** Optional supplementary text shown under the title. */
  subtitle?: string;
  /** Element rendered in the top-right of the header (e.g. action button). */
  trailing?: ReactNode;
  /** Tag override for the title; defaults to h2. */
  titleAs?: 'h2' | 'h3';
  /** Extra utility classes appended to the outer wrapper. */
  className?: string;
  children: ReactNode;
}

export function BentoCard({
  title,
  subtitle,
  trailing,
  titleAs = 'h2',
  className,
  children,
}: BentoCardProps): ReactElement {
  const TitleTag = titleAs;
  return (
    <section
      className={[
        'rounded-bento border border-moses-border bg-moses-surface-raised p-4',
        'shadow-bento transition-shadow hover:shadow-bento-hover',
        'dark:border-moses-border-dark dark:bg-moses-surface-dark-raised',
        className ?? '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      {(title || trailing) && (
        <header className="mb-4 flex items-start justify-between gap-4">
          <div>
            {title && (
              <TitleTag className="text-base font-semibold tracking-tight">{title}</TitleTag>
            )}
            {subtitle && (
              <p className="mt-1 text-sm text-moses-text-muted">{subtitle}</p>
            )}
          </div>
          {trailing && <div className="shrink-0">{trailing}</div>}
        </header>
      )}
      <div className="text-sm text-moses-text">{children}</div>
    </section>
  );
}

export default BentoCard;
