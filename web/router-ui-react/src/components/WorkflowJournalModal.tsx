import { useCallback, useEffect, useRef, useState } from 'react';
import type { RouterHttpClient, WorkflowJournalDetail, WorkflowJournalRun } from '../transport/http-client';
import { DialogShell } from './DialogShell';

function duration(ms: number | null | undefined): string {
  if (ms == null) return 'Unknown duration';
  const seconds = Math.floor(Math.max(0, ms) / 1000);
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor(seconds % 3600 / 60)}m`;
}
const message = (e: unknown) => e instanceof Error ? e.message : String(e);

export function WorkflowJournalModal({ agentId, client, onClose }: { agentId: string; client: RouterHttpClient; onClose: () => void }) {
  const runsScope = useRef(0);
  const detailScope = useRef(0);
  const [runs, setRuns] = useState<WorkflowJournalRun[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [status, setStatus] = useState('');
  const [workflow, setWorkflow] = useState('');
  const [selected, setSelected] = useState<string>();
  const [detail, setDetail] = useState<WorkflowJournalDetail>();
  const [loading, setLoading] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState('');
  const [confirm, setConfirm] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [version, setVersion] = useState(0);
  const filters = useCallback((next?: string) => ({ ...(status ? { status } : {}), ...(workflow ? { workflow_id: workflow } : {}), ...(next ? { cursor: next } : {}) }), [status, workflow]);
  useEffect(() => {
    let live = true;
    runsScope.current++;
    setLoading(true); setError(''); setRuns([]); setCursor(undefined); setSelected(undefined); setDetail(undefined);
    client.workflowJournalRuns(agentId, filters()).then(page => { if (live) { setRuns(page.runs); setCursor(page.next_cursor); } }).catch(e => { if (live) setError(message(e)); }).finally(() => { if (live) setLoading(false); });
    return () => { live = false; runsScope.current++; };
  }, [agentId, client, filters, version]);
  useEffect(() => {
    detailScope.current++;
    if (!selected) { setDetailLoading(false); return; }
    let live = true;
    setDetail(undefined); setDetailLoading(true); setError('');
    client.workflowJournalDetail(agentId, selected).then(page => { if (live) setDetail(page); }).catch(e => { if (live) setError(message(e)); }).finally(() => { if (live) setDetailLoading(false); });
    return () => { live = false; detailScope.current++; };
  }, [agentId, client, selected, filters, version]);
  const moreRuns = async () => {
    if (!cursor || loading) return;
    const scope = runsScope.current;
    setLoading(true); setError('');
    try { const page = await client.workflowJournalRuns(agentId, filters(cursor)); if (scope === runsScope.current) { setRuns(old => [...old, ...page.runs]); setCursor(page.next_cursor); } } catch (e) { if (scope === runsScope.current) setError(message(e)); } finally { if (scope === runsScope.current) setLoading(false); }
  };
  const moreEvents = async () => {
    if (!selected || !detail?.next_cursor || detailLoading) return;
    const scope = detailScope.current;
    setDetailLoading(true); setError('');
    try { const page = await client.workflowJournalDetail(agentId, selected, detail.next_cursor); if (scope === detailScope.current) setDetail(old => old ? { ...page, events: [...old.events, ...page.events] } : page); } catch (e) { if (scope === detailScope.current) setError(message(e)); } finally { if (scope === detailScope.current) setDetailLoading(false); }
  };
  const remove = async () => {
    if (!selected || deleting) return;
    setDeleting(true); setError('');
    try { await client.deleteWorkflowJournalRun(agentId, selected); setConfirm(false); setVersion(v => v + 1); } catch (e) { setError(message(e)); } finally { setDeleting(false); }
  };
  const events = detail?.events ?? [];
  const current = detail?.current_visit;
  const timeline = [...events.filter(e => e.kind !== 'note' && e.kind !== 'entry'), ...(current && !events.some(e => e.id === current.id) ? [current] : [])].sort((a, b) => a.sequence - b.sequence);
  const targetIds = new Set(timeline.map(event => event.id));
  const orphans = events.filter(event => (event.kind === 'note' || event.kind === 'entry') && (!event.target_event_id || !targetIds.has(event.target_event_id)));
  const visits = timeline.filter(e => e.state_id && ['start', 'visit', 'admin_visit', 'state_move', 'state_entry'].includes(e.kind));
  const distribution = new Map((detail?.state_totals ?? []).map(item => [JSON.stringify([item.workflow_id, item.state_id]), { name: `${item.workflow_name} / ${item.state_name}`, ms: item.duration_ms }]));
  const max = Math.max(1, ...Array.from(distribution.values(), x => x.ms));
  return <>
    <DialogShell open title="Workflow journal" onClose={onClose} closeLabel="Close workflow journal" className="message-dialog workflow-journal-dialog">
      <p>Terminal runs are retained up to 90 days; capacity limits may remove them sooner. Background agent deletion also removes its journal. Older unjournaled history cannot be reconstructed.</p>
      <p>Workflow filters match retained history only.</p>
      <div className="workflow-journal-filters"><label>Status <select value={status} onChange={e => setStatus(e.target.value)}><option value="">All</option><option value="active">Active</option><option value="terminal">Terminal</option></select></label><label>Workflow ID <input value={workflow} onChange={e => setWorkflow(e.target.value)} /></label><button type="button" onClick={() => setVersion(v => v + 1)}>Refresh</button></div>
      {error && !confirm ? <p role="alert">{error}</p> : null}
      <div className="workflow-journal-layout"><section aria-label="Runs"><h3>Runs</h3>{loading && !runs.length ? <p>Loading runs…</p> : !runs.length && !error ? <p>No retained runs.</p> : null}<ul className="workflow-journal-runs">{runs.map(run => <li key={run.id}><button type="button" aria-pressed={selected === run.id} onClick={() => setSelected(run.id)}>{run.workflow_name} · {run.status} · {new Date(run.started_at).toLocaleString()}</button></li>)}</ul>{cursor ? <button type="button" disabled={loading} onClick={() => void moreRuns()}>Load more runs</button> : null}</section>
      <section aria-label="Run details">{detailLoading && !detail ? <p>Loading run…</p> : null}{detail ? <><div className="workflow-journal-detail-heading"><h3>{detail.run.workflow_name}</h3>{detail.run.status === 'terminal' ? <button type="button" className="icon-btn-round" aria-label="Delete run" title="Delete run" onClick={() => setConfirm(true)}><i className="fa-solid fa-trash" aria-hidden="true" /></button> : null}</div>{detail.partial ? <p>Partial history and analytics{detail.cutoff_sequence ? `; retained history begins at sequence ${detail.cutoff_sequence}` : ''}.</p> : null}<h4>Time by state</h4>{distribution.size ? <ul className="workflow-journal-chart">{Array.from(distribution, ([key, item]) => <li key={key}><span>{item.name}</span><span>{duration(item.ms)}</span><div className="workflow-journal-bar" style={{ width: `${item.ms / max * 100}%` }} /></li>)}</ul> : <p>No measurable visits.</p>}<h4>Timeline</h4><ol className="workflow-journal-timeline">{timeline.map(visit => <li key={visit.id}><details><summary>{visit.state_name || visit.kind.replaceAll('_', ' ')}{visits.includes(visit) ? ` · ${duration(visit.duration_ms)}` : ''}</summary><p>{visit.workflow_name} · {new Date(visit.occurred_at).toLocaleString()}</p>{visit.reason ? <p>{visit.reason}</p> : null}{visit.text ? <p>{visit.text}</p> : null}{detail.events.filter(event => event.target_event_id === visit.id).map(note => <p key={note.id}>{note.source}: {note.text}</p>)}</details></li>)}</ol>{orphans.length ? <section aria-label="Notes with missing targets"><h4>Notes with missing targets</h4><p>The target is not in the loaded history; it may have been removed or not yet loaded.</p><ul>{orphans.map(note => <li key={note.id}>{note.source}: {note.text}</li>)}</ul></section> : null}{detail.next_cursor ? <button type="button" disabled={detailLoading} onClick={() => void moreEvents()}>Load more visits</button> : null}</> : null}</section></div>
    </DialogShell>
    {confirm && detail ? <DialogShell open title="Delete run" description={`Delete ${detail.run.workflow_name} run ${detail.run.id}? Its timeline and notes will be permanently removed.`} onClose={() => { if (!deleting) setConfirm(false); }} className="message-dialog delete-confirm-dialog">{error ? <p role="alert">{error}</p> : null}<div className="dialog-actions workflow-dialog-actions"><button type="button" className="btn btn-secondary" disabled={deleting} onClick={() => setConfirm(false)}>Cancel</button><button type="button" className="btn btn-danger" disabled={deleting} onClick={() => void remove()}>{deleting ? 'Deleting…' : 'Delete permanently'}</button></div></DialogShell> : null}
  </>;
}
