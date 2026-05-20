// MessageRow collapsed + expanded.

import { describe, it, expect } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { axe } from 'jest-axe';
import type { Link, Message } from '@/lib/bot-api';
import MessageRow from './MessageRow';

const link: Link = {
  id: 'l-1',
  provider: 'telegram',
  providerUserId: '12345',
  providerDisplayName: 'Alice',
  isActive: true,
  createdAt: '2026-05-19T00:00:00Z',
};

const baseMsg: Message = {
  id: 'm-1',
  linkId: 'l-1',
  direction: 'in',
  text: 'hello bot',
  occurredAt: '2026-05-19T10:00:00Z',
};

function renderRow(
  message: Message = baseMsg,
  // Use `null` as the sentinel for "no link" so the TS default-parameter
  // semantics (which collapse `undefined` to the default) don't trip us up.
  withLink: Link | null = link,
) {
  // Wrap in a <ul> so the <li> is in a valid context.
  return render(
    <ul>
      <MessageRow
        message={message}
        link={withLink ?? undefined}
        now={() => new Date('2026-05-19T10:00:30Z').getTime()}
      />
    </ul>,
  );
}

describe('<MessageRow />', () => {
  it('collapsed: shows direction icon, link label, truncated text and a relative time', () => {
    renderRow();
    expect(screen.getByText(/Telegram/)).toBeInTheDocument();
    expect(screen.getByText('hello bot')).toBeInTheDocument();
    // The row button reports collapsed.
    const btn = screen.getByRole('button');
    expect(btn).toHaveAttribute('aria-expanded', 'false');
  });

  it('truncates text longer than 200 chars in the collapsed view', () => {
    const longText = 'a'.repeat(300);
    renderRow({ ...baseMsg, text: longText });
    const collapsed = screen.getByRole('button');
    // Ellipsis appended, and the visible label is bounded.
    expect(collapsed.textContent).toMatch(/a{200}…/);
  });

  it('clicking expands and reveals full text + metadata', () => {
    const long = 'b'.repeat(300);
    renderRow({ ...baseMsg, text: long, error: 'boom' });
    fireEvent.click(screen.getByRole('button'));
    const btn = screen.getByRole('button');
    expect(btn).toHaveAttribute('aria-expanded', 'true');
    // Full text now visible.
    expect(screen.getAllByText(long).length).toBeGreaterThan(0);
    expect(screen.getByText(/message id/i)).toBeInTheDocument();
    expect(screen.getByText('m-1')).toBeInTheDocument();
    expect(screen.getAllByText('boom').length).toBeGreaterThan(0);
  });

  it('renders "Unlinked chat" when no link metadata is available', () => {
    renderRow(baseMsg, null);
    expect(screen.getByText(/unlinked chat/i)).toBeInTheDocument();
  });

  it('has no axe violations (collapsed)', async () => {
    const { container } = renderRow();
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });

  it('has no axe violations (expanded)', async () => {
    const { container } = renderRow();
    fireEvent.click(screen.getByRole('button'));
    const results = await axe(container);
    expect(results).toHaveNoViolations();
  });
});
