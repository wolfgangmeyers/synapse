// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { ProgressHistoryModal } from '../../src/components/ProgressHistoryModal';

describe('ProgressHistoryModal', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn());
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it('does not render when isOpen is false', () => {
    render(<ProgressHistoryModal agentId="agent-1" isOpen={false} onClose={() => {}} />);
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('renders history, time window selector, reversed order, and expandable entries without PNG link', async () => {
    const mockData = [
      { percent: 25, note: 'First step milestone', timestamp: '2026-10-18T00:00:00Z' },
      { percent: 75, note: 'Second step with a long explanatory progress description', timestamp: '2026-10-18T00:30:00Z' },
    ];
    (globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => mockData,
    });

    render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={() => {}} />);
    expect(screen.getByRole('dialog')).toBeDefined();
    expect(screen.getByText('Loading progress history...')).toBeDefined();

    await waitFor(() => {
      expect(screen.queryByText('Loading progress history...')).toBeNull();
    });

    // Verify Server PNG link is NOT present
    expect(screen.queryByText('Open Server PNG')).toBeNull();

    // Verify all 8 time window pills exist
    const windowTabs = screen.getAllByRole('tab');
    expect(windowTabs.map((t) => t.textContent)).toEqual([
      '1h', '3h', '6h', '12h', '24h', '3d', '7d', 'All'
    ]);
    // 1h is default active
    const oneHourTab = screen.getByRole('tab', { name: '1h' });
    expect(oneHourTab.getAttribute('aria-selected')).toBe('true');

    // Verify reverse chronological order (most recent 'Second step' first)
    const entryNotes = document.querySelectorAll('.progress-history-entry .note');
    expect(entryNotes.length).toBe(2);
    expect(entryNotes[0]?.textContent).toContain('Second step');
    expect(entryNotes[1]?.textContent).toContain('First step');

    // Test click to expand
    const firstEntry = document.querySelectorAll('.progress-history-entry')[0];
    expect(firstEntry).toBeDefined();
    expect(entryNotes[0]).toHaveClass('note-collapsed');

    fireEvent.click(firstEntry!);
    expect(entryNotes[0]).toHaveClass('note-expanded');

    fireEvent.click(firstEntry!);
    expect(entryNotes[0]).toHaveClass('note-collapsed');

    // Test switching window to 24h
    const tab24h = screen.getByRole('tab', { name: '24h' });
    fireEvent.click(tab24h);
    expect(tab24h.getAttribute('aria-selected')).toBe('true');
  });

  it('renders empty message when history is empty', async () => {
    (globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => [],
    });

    render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText('No progress history recorded')).toBeDefined();
    });
  });

  it('calls onClose when close button clicked or Escape pressed', async () => {
    (globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => [],
    });
    const onClose = vi.fn();
    render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={onClose} />);

    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalledTimes(1);

    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
