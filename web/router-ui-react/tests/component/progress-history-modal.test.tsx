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

  it('verifies polyline includes earlier entry extending off-screen to the left (X < 44) with clipPath', async () => {
    // 1h window duration is 3600*1000 ms.
    // Latest entry at 12:00:00.
    // minTime for 1h is 11:00:00.
    // Entry inside window at 11:30:00.
    // Entry prior to window at 10:00:00 (1 hour prior to window start).
    const mockData = [
      { percent: 10, note: 'Prior entry', timestamp: '2026-10-18T10:00:00Z' },
      { percent: 50, note: 'Window entry 1', timestamp: '2026-10-18T11:30:00Z' },
      { percent: 90, note: 'Window entry 2', timestamp: '2026-10-18T12:00:00Z' },
    ];

    (globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => mockData,
    });

    render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.queryByText('Loading progress history...')).toBeNull();
    });

    const polyline = document.querySelector('polyline');
    expect(polyline).not.toBeNull();
    expect(
      polyline?.getAttribute('clipPath') ?? polyline?.getAttribute('clip-path')
    ).toBe('url(#chart-plot-clip)');

    const points = polyline?.getAttribute('points');
    expect(points).toBeTruthy();

    const coords = points!.trim().split(/\s+/).map((pt) => {
      const [x, y] = pt.split(',').map(Number);
      return { x, y };
    });

    // Should include prior entry plus the 2 window entries (3 total)
    expect(coords.length).toBe(3);

    // Prior entry timestamp is 1 hour before window start (window duration is 1 hour, width is 436px).
    // ratio = (10:00 - 11:00) / 1hr = -1.
    // X = 44 + (-1) * 436 = -392.
    // It should extend to the left of the plot area (X < 44).
    expect(coords[0]?.x).toBeLessThan(44);
    // And window entries should have X >= 44
    expect(coords[1]?.x).toBeGreaterThanOrEqual(44);
    expect(coords[2]?.x).toBeGreaterThanOrEqual(44);
  });

  it('supports keyboard navigation expanding notes with Enter and Space keys', async () => {
    const mockData = [
      { percent: 50, note: 'Keyboard toggleable note', timestamp: '2026-10-18T00:00:00Z' },
    ];
    (globalThis.fetch as any).mockResolvedValueOnce({
      ok: true,
      json: async () => mockData,
    });

    render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.queryByText('Loading progress history...')).toBeNull();
    });

    const entry = screen.getByRole('button', { name: /50%/i });
    const note = entry.querySelector('.note');
    expect(note).toHaveClass('note-collapsed');

    // Press Enter to expand
    fireEvent.keyDown(entry, { key: 'Enter' });
    expect(note).toHaveClass('note-expanded');

    // Press Enter to collapse
    fireEvent.keyDown(entry, { key: 'Enter' });
    expect(note).toHaveClass('note-collapsed');

    // Press Space to expand
    fireEvent.keyDown(entry, { key: ' ' });
    expect(note).toHaveClass('note-expanded');

    // Press Space to collapse
    fireEvent.keyDown(entry, { key: ' ' });
    expect(note).toHaveClass('note-collapsed');
  });

  it('renders error state banner when fetch rejects or returns non-ok response', async () => {
    // 1. Rejection / network failure
    (globalThis.fetch as any).mockRejectedValueOnce(new Error('Network error loading history'));

    const { unmount } = render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText('Network error loading history')).toBeDefined();
    });
    unmount();

    // 2. Non-ok response
    (globalThis.fetch as any).mockResolvedValueOnce({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
    });

    render(<ProgressHistoryModal agentId="agent-1" isOpen={true} onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText('Failed to load progress history: Internal Server Error')).toBeDefined();
    });
  });

  it('uses injected client if provided', async () => {
    const mockData = [
      { percent: 80, note: 'Client entry', timestamp: '2026-10-18T01:00:00Z' },
    ];
    const mockClient = {
      progressHistory: vi.fn().mockResolvedValue(mockData),
    };

    render(<ProgressHistoryModal agentId="agent-custom" isOpen={true} onClose={() => {}} client={mockClient as any} />);

    await waitFor(() => {
      expect(screen.queryByText('Loading progress history...')).toBeNull();
    });

    expect(mockClient.progressHistory).toHaveBeenCalledWith('agent-custom');
    expect(screen.getByText('Client entry')).toBeDefined();
  });
});

