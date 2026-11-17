// @vitest-environment jsdom
import { afterEach, expect, it } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MessageList } from '../../src/components/MessageList';
import type { ConversationTimelineEvent } from '../../src/services/api';

afterEach(cleanup);

it('resets the expanded history window when the owner changes with the same router and peer', () => {
  const timeline: ConversationTimelineEvent[] = Array.from({ length: 60 }, (_, i) => ({
    id: `msg:agent:item-${i}`, agent_id: 'agent', kind: 'message', role: 'assistant',
    seq: i + 1, created_at: Date.parse('2026-01-01T00:00:00Z') + i * 1000, content: `item-${i}`,
  }));
  const props = { routerUrl: 'http://localhost:8765', peerId: 'agent', timelineEvents: timeline, historyWindowReady: true };
  const { container, rerender } = render(<MessageList {...props} ownerId="alice" />);
  expect(screen.queryByText('item-0')).not.toBeInTheDocument();

  const list = container.querySelector<HTMLDivElement>('.messages');
  if (!list) throw new Error('Messages container not found');
  Object.defineProperty(list, 'clientHeight', { configurable: true, value: 200 });
  Object.defineProperty(list, 'scrollHeight', { configurable: true, value: 500 });

  list.scrollTop = 50;
  fireEvent.scroll(list);
  expect(screen.getByText('item-0')).toBeInTheDocument();

  rerender(<MessageList {...props} ownerId="bob" />);
  expect(screen.queryByText('item-0')).not.toBeInTheDocument();
  expect(screen.getByText('item-59')).toBeInTheDocument();

  // Verify that older history is available again after reset and can be revealed via scroll-up
  list.scrollTop = 50;
  fireEvent.scroll(list);
  expect(screen.getByText('item-0')).toBeInTheDocument();
});
