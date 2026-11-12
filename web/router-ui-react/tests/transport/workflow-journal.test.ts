import { describe, expect, it, vi } from 'vitest';
import { createHttpClient } from '../../src/transport/http-client';

describe('workflow journal HTTP', () => {
  it('uses encoded managed agent routes and scoped pagination filters', async () => {
    const fetcher = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ runs: [], next_cursor: 'next' }), { status: 200, headers: { 'Content-Type': 'application/json' } })));
    const client = createHttpClient('http://router.test', fetcher);
    await client.workflowJournalRuns('a/b', { status: 'terminal', workflow_id: 'w/1', cursor: 'a+b' });
    expect(String(fetcher.mock.calls[0]?.[0])).toBe('http://router.test/v1/managed-agents/a%2Fb/workflow-journal?status=terminal&workflow_id=w%2F1&cursor=a%2Bb');
    await client.workflowJournalDetail('a/b', 'r/1', 'next');
    expect(String(fetcher.mock.calls[1]?.[0])).toBe('http://router.test/v1/managed-agents/a%2Fb/workflow-journal/r%2F1?cursor=next');
    await client.deleteWorkflowJournalRun('a/b', 'r/1');
    expect(fetcher.mock.calls[2]?.[1]).toMatchObject({ method: 'DELETE', credentials: 'omit' });
  });
});
