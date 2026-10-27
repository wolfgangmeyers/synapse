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

  it('renders and fetches history when open', async () => {
    const mockData = [
      { percent: 25, note: 'Step 1', timestamp: '2026-10-18T00:00:00Z' },
      { percent: 75, note: 'Step 2', timestamp: '2026-10-18T01:00:00Z' },
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

    expect(screen.getByText('Open Server PNG')).toBeDefined();
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
