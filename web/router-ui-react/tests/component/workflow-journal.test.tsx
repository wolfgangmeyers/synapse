import React from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { WorkflowJournalModal } from '../../src/components/WorkflowJournalModal';
import type { RouterHttpClient } from '../../src/transport/http-client';

afterEach(cleanup);
const run = { id: 'run-1', workflow_id: 'w', workflow_name: 'Build', status: 'terminal' as const, started_at: '2026-01-01T00:00:00Z' };
const detail = { run, events: [
  { id: 'v1', sequence: 1, kind: 'state_entry', workflow_id: 'w', workflow_name: 'Build', state_id: 's', state_name: 'Plan', occurred_at: '2026-01-01T00:00:00Z', duration_ms: 3000, reason: '<unsafe>' },
  { id: 'n', sequence: 2, kind: 'note', target_event_id: 'v1', text: 'A note', source: 'proactive', occurred_at: '2026-01-01T00:00:01Z' },
  { id: 'v2', sequence: 3, kind: 'state_entry', workflow_id: 'w', workflow_name: 'Build', state_id: 's', state_name: 'Plan', occurred_at: '2026-01-01T00:00:03Z', duration_ms: 2000 },
  { id: 'v3', sequence: 4, kind: 'state_entry', workflow_id: 'other', workflow_name: 'Other', state_id: 's', state_name: 'Plan', occurred_at: '2026-01-01T00:00:05Z', duration_ms: 1000 },
], state_totals: [{ workflow_id: 'w', workflow_name: 'Build', state_id: 's', state_name: 'Plan', duration_ms: 5000 }, { workflow_id: 'other', workflow_name: 'Other', state_id: 's', state_name: 'Plan', duration_ms: 1000 }], partial: true, cutoff_sequence: 1 };
function setup() {
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [run] }), workflowJournalDetail: vi.fn().mockResolvedValue(detail), deleteWorkflowJournalRun: vi.fn().mockResolvedValue(undefined) };
  render(<WorkflowJournalModal agentId="agent/1" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  return client;
}
describe('workflow journal', () => {
  it('loads an agent-scoped run and shows measurable visit durations and separate distribution identities', async () => {
    const client = setup();
    fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
    expect(client.workflowJournalRuns).toHaveBeenCalledWith('agent/1', expect.anything());
    expect(await screen.findByText(/partial/i)).toBeTruthy();
    expect(screen.getByText('5s')).toBeTruthy();
    expect(screen.getAllByText(/Plan/).length).toBeGreaterThan(1);
    fireEvent.click(screen.getAllByText(/Plan.*3s/i)[0]!);
    expect(screen.getByText('<unsafe>')).toBeTruthy();
    expect(screen.getByText(/A note/)).toBeTruthy();
  });
  it('confirms terminal deletion, allows cancel, and refreshes', async () => {
    const client = setup();
    fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
    fireEvent.click(await screen.findByRole('button', { name: /delete run/i }));
    expect(screen.getByText(/permanently removed/i)).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(client.deleteWorkflowJournalRun).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: /delete run/i }));
    fireEvent.click(screen.getByRole('button', { name: 'Delete permanently' }));
    await waitFor(() => expect(client.deleteWorkflowJournalRun).toHaveBeenCalledWith('agent/1', 'run-1'));
    await waitFor(() => expect(client.workflowJournalRuns).toHaveBeenCalledTimes(2));
  });
});

it('does not offer deletion for an active run and reports load errors', async () => {
  const active = { ...run, status: 'active' as const };
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [active] }), workflowJournalDetail: vi.fn().mockResolvedValue({ ...detail, run: active }), deleteWorkflowJournalRun: vi.fn() };
  const view = render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: /Build.*active/i }));
  await screen.findByText(/partial/i);
  expect(screen.queryByRole('button', { name: /delete run/i })).toBeNull();
  view.unmount();
  client.workflowJournalRuns.mockRejectedValueOnce(new Error('Offline'));
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  expect((await screen.findByRole('alert')).textContent).toContain('Offline');
});

it('shows terminal events and their prompted entries in the timeline', async () => {
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [run] }), workflowJournalDetail: vi.fn().mockResolvedValue({ ...detail, events: [...detail.events,
    { id: 'exit', sequence: 5, kind: 'exit', occurred_at: '2026-01-01T00:00:06Z', reason: 'Finished' },
    { id: 'entry', sequence: 6, kind: 'entry', target_event_id: 'exit', source: 'prompted', text: 'Next steps', occurred_at: '2026-01-01T00:00:07Z' },
  ] }), deleteWorkflowJournalRun: vi.fn() };
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
  fireEvent.click(await screen.findByText(/exit/i));
  expect(screen.getByText('Finished')).toBeTruthy();
  expect(screen.getByText(/Next steps/)).toBeTruthy();
  expect(screen.getByText(/filters match retained/i)).toBeTruthy();
});

it.each([false, true])('includes current_visit once in timeline and chart (retained=%s)', async retained => {
  const current = { id: 'current', sequence: 7, kind: 'visit', workflow_id: 'w', workflow_name: 'Build', state_id: 'current-state', state_name: 'Current', occurred_at: '2026-01-01T00:00:07Z', duration_ms: 11000 };
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [run] }), workflowJournalDetail: vi.fn().mockResolvedValue({ ...detail, events: retained ? [...detail.events, current] : detail.events, current_visit: current, state_totals: [...detail.state_totals, { workflow_id: 'w', workflow_name: 'Build', state_id: 'current-state', state_name: 'Current', duration_ms: 11000 }] }), deleteWorkflowJournalRun: vi.fn() };
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
  expect(await screen.findByText('Current · 11s')).toBeTruthy();
  expect(screen.getAllByText('11s')).toHaveLength(1);
  expect(screen.getAllByText('Current · 11s')).toHaveLength(1);
});

it('discloses retained notes whose targets are missing, without orphaning current visit notes', async () => {
  const current = { id: 'current', sequence: 9, kind: 'visit', state_name: 'Current', occurred_at: '2026-01-01T00:00:07Z' };
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [run] }), workflowJournalDetail: vi.fn().mockResolvedValue({ ...detail, current_visit: current, events: [...detail.events, { id: 'lost', sequence: 7, kind: 'note', target_event_id: 'evicted', source: 'proactive', text: 'Retained orphan' }, { id: 'cn', sequence: 8, kind: 'note', target_event_id: 'current', source: 'proactive', text: 'Current note' }] }), deleteWorkflowJournalRun: vi.fn() };
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
  expect(await screen.findByText(/missing target/i)).toBeTruthy();
  expect(screen.getByText(/Retained orphan/)).toBeTruthy();
  fireEvent.click(screen.getByText('Current'));
  expect(screen.getByText(/Current note/)).toBeTruthy();
});

it('ignores a late runs page after the filter changes', async () => {
  let resolve!: (value: unknown) => void;
  const pending = new Promise(r => { resolve = r; });
  const client = { workflowJournalRuns: vi.fn().mockResolvedValueOnce({ runs: [run], next_cursor: 'next' }).mockReturnValueOnce(pending).mockResolvedValue({ runs: [] }), workflowJournalDetail: vi.fn(), deleteWorkflowJournalRun: vi.fn() };
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: 'Load more runs' }));
  fireEvent.change(screen.getByLabelText('Status'), { target: { value: 'active' } });
  await screen.findByText('No retained runs.');
  resolve({ runs: [run] });
  await new Promise(r => setTimeout(r, 0));
  expect(screen.queryByRole('button', { name: /Build.*terminal/i })).toBeNull();
});

it('ignores a late visits page after selecting another run', async () => {
  let resolve!: (value: unknown) => void;
  const pending = new Promise(r => { resolve = r; });
  const other = { ...run, id: 'run-2', workflow_name: 'Deploy' };
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [run, other] }), workflowJournalDetail: vi.fn().mockResolvedValueOnce({ ...detail, next_cursor: 'next' }).mockReturnValueOnce(pending).mockResolvedValueOnce({ run: other, events: [], partial: false }), deleteWorkflowJournalRun: vi.fn() };
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
  fireEvent.click(await screen.findByRole('button', { name: 'Load more visits' }));
  fireEvent.click(screen.getByRole('button', { name: /Deploy.*terminal/i }));
  await screen.findByText('No measurable visits.');
  resolve({ ...detail, events: [{ ...detail.events[0], state_name: 'Stale visit' }] });
  await new Promise(r => setTimeout(r, 0));
  expect(screen.queryByText(/Stale visit/)).toBeNull();
  expect(screen.getByRole('heading', { name: 'Deploy' })).toBeTruthy();
});

it('shows full retained totals independently of timeline paging and discloses actual truncation', async () => {
  const client = { workflowJournalRuns: vi.fn().mockResolvedValue({ runs: [run] }), workflowJournalDetail: vi.fn().mockResolvedValueOnce({ ...detail, events: detail.events.slice(0, 1), next_cursor: 'next', partial: false }).mockResolvedValueOnce({ ...detail, events: detail.events.slice(1), partial: true, cutoff_sequence: 5 }), deleteWorkflowJournalRun: vi.fn() };
  render(<WorkflowJournalModal agentId="a" client={client as unknown as RouterHttpClient} onClose={() => {}} />);
  fireEvent.click(await screen.findByRole('button', { name: /Build.*terminal/i }));
  expect(await screen.findByText('5s')).toBeTruthy();
  expect(screen.queryByText(/partial history/i)).toBeNull();
  fireEvent.click(screen.getByRole('button', { name: /load more visits/i }));
  expect(await screen.findByText(/retained history begins at sequence 5/i)).toBeTruthy();
  expect(screen.getByText('5s')).toBeTruthy();
});
