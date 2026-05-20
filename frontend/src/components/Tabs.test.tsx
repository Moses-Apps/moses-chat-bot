// Tabs primitive — keyboard navigation (ArrowLeft/Right/Home/End) and
// roving-tabindex semantics.
//
// jsdom renders both the <select> (sm-) and the tablist (sm+); the tablist is
// the keyboard-accessible surface, so we drive it directly.

import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, within } from '@testing-library/react';
import Tabs, { type TabItem } from './Tabs';

function makeItems(): TabItem[] {
  return [
    { id: 'one', label: 'One', content: <p>one body</p> },
    { id: 'two', label: 'Two', content: <p>two body</p> },
    { id: 'three', label: 'Three', content: <p>three body</p> },
  ];
}

function renderWithState(initial = 'one') {
  let value = initial;
  const onChange = vi.fn((id: string) => {
    value = id;
  });
  const items = makeItems();
  const utils = render(<Tabs ariaLabel="Test tabs" items={items} value={value} onChange={onChange} />);
  return { ...utils, onChange, items, getValue: () => value };
}

describe('<Tabs /> keyboard navigation', () => {
  it('ArrowRight moves selection to the next tab and wraps at end', () => {
    const { onChange } = renderWithState('one');
    const list = screen.getByRole('tablist');
    const tabs = within(list).getAllByRole('tab');
    tabs[0].focus();
    fireEvent.keyDown(tabs[0], { key: 'ArrowRight' });
    expect(onChange).toHaveBeenLastCalledWith('two');
    // Wrap-around from last → first.
    onChange.mockClear();
    // Re-render with last selected so the active tab gets a fresh handler.
    const { rerender } = render(
      <Tabs ariaLabel="Test tabs" items={makeItems()} value="three" onChange={onChange} />,
    );
    rerender(<Tabs ariaLabel="Test tabs" items={makeItems()} value="three" onChange={onChange} />);
    const lists = screen.getAllByRole('tablist');
    const second = within(lists[lists.length - 1]).getAllByRole('tab');
    second[2].focus();
    fireEvent.keyDown(second[2], { key: 'ArrowRight' });
    expect(onChange).toHaveBeenLastCalledWith('one');
  });

  it('ArrowLeft moves selection to the previous tab and wraps at start', () => {
    const { onChange } = renderWithState('two');
    const list = screen.getByRole('tablist');
    const tabs = within(list).getAllByRole('tab');
    fireEvent.keyDown(tabs[1], { key: 'ArrowLeft' });
    expect(onChange).toHaveBeenLastCalledWith('one');
  });

  it('Home jumps to the first tab and End jumps to the last', () => {
    const { onChange } = renderWithState('two');
    const list = screen.getByRole('tablist');
    const tabs = within(list).getAllByRole('tab');
    fireEvent.keyDown(tabs[1], { key: 'End' });
    expect(onChange).toHaveBeenLastCalledWith('three');
    fireEvent.keyDown(tabs[1], { key: 'Home' });
    expect(onChange).toHaveBeenLastCalledWith('one');
  });

  it('roving tabindex: only the active tab has tabIndex=0', () => {
    renderWithState('two');
    const list = screen.getByRole('tablist');
    const tabs = within(list).getAllByRole('tab');
    expect(tabs[0]).toHaveAttribute('tabIndex', '-1');
    expect(tabs[1]).toHaveAttribute('tabIndex', '0');
    expect(tabs[2]).toHaveAttribute('tabIndex', '-1');
  });

  it('unrelated keys (Tab, Enter, Space) are not intercepted', () => {
    const { onChange } = renderWithState('one');
    const list = screen.getByRole('tablist');
    const tabs = within(list).getAllByRole('tab');
    fireEvent.keyDown(tabs[0], { key: 'Tab' });
    fireEvent.keyDown(tabs[0], { key: 'Enter' });
    fireEvent.keyDown(tabs[0], { key: ' ' });
    expect(onChange).not.toHaveBeenCalled();
  });
});
