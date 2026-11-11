import { useEffect, useState } from "react";
import type { BackgroundBashJob } from "../protocol/types";
import { HttpRequestError, type RouterHttpClient } from "../transport/http-client";
import { DialogShell } from "./DialogShell";

type Detail = Awaited<ReturnType<RouterHttpClient["backgroundBashDetail"]>>;
type Result = { agentId: string; client: RouterHttpClient; id: string; status: string; value: Detail | "unavailable" | "error" };

function jobLabel(job: BackgroundBashJob, index: number) {
  const date = job.started_at ? new Date(job.started_at) : null;
  return date && !Number.isNaN(date.getTime())
    ? `Started ${date.toLocaleString()} (${String(date.getMilliseconds()).padStart(3, "0")} ms)`
    : `Shell job ${index + 1}`;
}

type JobFilter = "active" | "completed" | "failed";
function jobCategory(status: string): JobFilter {
  if (status === "running" || status === "uncertain") return "active";
  return status === "succeeded" ? "completed" : "failed";
}

export function BackgroundBashActivity({ jobs = [], agentId, client }: { jobs: BackgroundBashJob[] | undefined; agentId?: string; client?: RouterHttpClient | undefined }) {
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState<JobFilter>("active");
  const [selected, setSelected] = useState<string | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const [scope, setScope] = useState({ agentId, client });
  if (scope.agentId !== agentId || scope.client !== client) {
    setScope({ agentId, client });
    setResult(null);
  }
  const [retry, setRetry] = useState(0);
  const job = jobs.find(item => item.id === selected);
  useEffect(() => {
    if (!open || !client || !agentId || !job) return;
    let current = true;
    let controller: AbortController | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let timeout: ReturnType<typeof setTimeout> | undefined;
    const refresh = async () => {
      controller = new AbortController();
      try {
        const value = await Promise.race([
          client.backgroundBashDetail(agentId, job.id, controller.signal),
          new Promise<never>((_, reject) => { timeout = setTimeout(() => { controller?.abort(); reject(new Error("Timed out")); }, 5000); }),
        ]);
        if (current) {
          setResult({ agentId, client, id: job.id, status: job.status, value });
          if (job.status === "running") timer = setTimeout(() => { void refresh(); }, 2000);
        }
      } catch (error) {
        if (current) setResult({ agentId, client, id: job.id, status: job.status, value: error instanceof HttpRequestError && error.status === 404 ? "unavailable" : "error" });
      } finally {
        if (timeout !== undefined) clearTimeout(timeout);
      }
    };
    void refresh();
    return () => { current = false; controller?.abort(); if (timer !== undefined) clearTimeout(timer); if (timeout !== undefined) clearTimeout(timeout); };
  }, [open, client, agentId, job?.id, job?.status, retry]);
  const active = jobs.filter(item => item.status === "running" || item.status === "uncertain").length;
  const detail = job && result && result.agentId === agentId && result.client === client && result.id === job.id && result.status === job.status ? result.value : undefined;
  const sortedJobs = jobs.map((item, index) => ({ item, index, time: item.started_at ? Date.parse(item.started_at) : NaN }))
    .sort((a, b) => (Number.isNaN(b.time) ? -Infinity : b.time) - (Number.isNaN(a.time) ? -Infinity : a.time) || a.index - b.index);
  const visibleJobs = sortedJobs.filter(({ item }) => jobCategory(item.status) === filter);
  const counts = { active: 0, completed: 0, failed: 0 };
  jobs.forEach(item => { counts[jobCategory(item.status)]++; });
  const close = () => { setOpen(false); setSelected(null); setResult(null); };
  return <section aria-label="Background shell jobs">
    <button type="button" className={`header-metric-card subagents-card clickable${active > 0 ? " active" : ""}`} onClick={() => { setFilter("active"); setOpen(true); }}>
      <span className="header-metric-icon subagents-icon"><i className="fa-solid fa-terminal" aria-hidden="true" /></span>
      <span className="header-metric-center"><span className="header-metric-title">Shell jobs</span></span>
      <span className="header-metric-right"><span className="header-metric-value">{active}</span></span>
    </button>
    <DialogShell open={open} title="Background shell jobs" onClose={close} className="subagent-viewer-dialog message-dialog">
      <div className="subagent-viewer-body">
        {selected !== null ? <div className="subagent-drilldown-view">
          <header className="subagent-drilldown-header">
            <button type="button" className="icon-btn-round subagent-back-button" aria-label="Back to shell jobs" onClick={() => { setSelected(null); setResult(null); }}><i className="fa-solid fa-arrow-left" aria-hidden="true" /></button>
            <div className="subagent-drilldown-info">{job ? <span>{jobLabel(job, jobs.indexOf(job))}: {job.status}</span> : <span>Job unavailable or expired.</span>}</div>
          </header>
          {job && <div className="subagent-conversation-view background-bash-detail">
            {detail === "unavailable" ? <p>Output unavailable or expired.</p> : detail === "error" ? <p>Could not load output. <button type="button" onClick={() => setRetry(n => n + 1)}>Retry</button></p> : detail ? <>
              {job.status !== "running" && job.status !== "uncertain" && detail.exitCode !== null && <div>Exit code: {detail.exitCode}</div>}
              {detail.truncated && <div>Output truncated at the capture limit.</div>}
              <div className="markdown-body"><pre><code>{detail.output}</code></pre></div>
            </> : <p>Loading output…</p>}
          </div>}
        </div> : <div className="subagent-list-view">
          <div className="subagent-list-toolbar"><label htmlFor="shell-job-filter" className="subagent-filter-label">Filter:</label><select id="shell-job-filter" className="subagent-filter-select" value={filter} onChange={event => setFilter(event.target.value as JobFilter)}><option value="active">Active ({counts.active})</option><option value="completed">Completed ({counts.completed})</option><option value="failed">Failed ({counts.failed})</option></select></div>
          {visibleJobs.length === 0 ? <div className="subagent-empty-state"><p>{jobs.length === 0 ? "No retained shell jobs." : "No shell jobs match the selected filter."}</p></div> : <ul className="subagent-card-grid background-bash-job-list">{visibleJobs.map(({ item, index }) => <li key={item.id}><button type="button" className={`subagent-card${item.status === "running" || item.status === "uncertain" ? " active" : ""}`} onClick={() => { setResult(null); setSelected(item.id); }}><span>{jobLabel(item, index)}</span><span>{item.status}</span></button></li>)}</ul>}
        </div>}
      </div>
    </DialogShell>
  </section>;
}
