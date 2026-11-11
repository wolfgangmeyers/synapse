import { useEffect, useRef, useState } from "react";
import type { BackgroundBashJob } from "../protocol/types";
import { HttpRequestError, type RouterHttpClient } from "../transport/http-client";
import { DialogShell } from "./DialogShell";

type Detail = Awaited<ReturnType<RouterHttpClient["backgroundBashDetail"]>>;

export function BackgroundBashActivity({ jobs = [], agentId, client }: { jobs: BackgroundBashJob[] | undefined; agentId?: string; client?: RouterHttpClient | undefined }) {
  const [open, setOpen] = useState(false);
  const [details, setDetails] = useState<Record<string, { status: string; value: Detail | "unavailable" | "error" }>>({});
  const [retry, setRetry] = useState(0);
  const cache = useRef<Record<string, { status: string; value: Detail | "unavailable" | "error" }>>({});
  const key = JSON.stringify(jobs);
  useEffect(() => {
    cache.current = {};
    setDetails({});
  }, [open, client, agentId]);
  useEffect(() => {
    if (!open || !client || !agentId) return;
    let current = true;
    const controllers = new Set<AbortController>();
    let timer: ReturnType<typeof setTimeout> | undefined;
    for (const id of Object.keys(cache.current)) if (!jobs.some(job => job.id === id)) delete cache.current[id];
    setDetails(previous => {
      const next: typeof previous = {};
      for (const job of jobs) {
        const saved = cache.current[job.id];
        if (saved && saved.status === job.status) next[job.id] = saved;
      }
      return next;
    });
    const refresh = async () => {
      const pending = jobs.filter(job => {
        const saved = cache.current[job.id];
        if (!saved || saved.status !== job.status) return true;
        return typeof saved.value === "object" && (job.status === "running" || job.status === "uncertain");
      });
      let next = 0;
      const worker = async () => {
        while (current && next < pending.length) {
          const job = pending[next++];
          if (!job) break;
          const controller = new AbortController();
          controllers.add(controller);
          let timeout: ReturnType<typeof setTimeout> | undefined;
          try {
            const detail = await Promise.race([
              client.backgroundBashDetail(agentId, job.id, controller.signal),
              new Promise<never>((_, reject) => { timeout = setTimeout(() => { controller.abort(); reject(new Error("Timed out")); }, 5000); }),
            ]);
            if (current) {
              cache.current[job.id] = { status: job.status, value: detail };
              setDetails(previous => ({ ...previous, [job.id]: { status: job.status, value: detail } }));
            }
          } catch (error) {
            if (current) {
              const value = error instanceof HttpRequestError && error.status === 404 ? "unavailable" : "error";
              cache.current[job.id] = { status: job.status, value };
              setDetails(previous => ({ ...previous, [job.id]: { status: job.status, value } }));
            }
          } finally {
            controllers.delete(controller);
            if (timeout !== undefined) clearTimeout(timeout);
          }
        }
      };
      await Promise.all(Array.from({ length: Math.min(4, pending.length) }, worker));
      if (current && jobs.some(job => job.status === "running" || job.status === "uncertain")) timer = setTimeout(refresh, 2000);
    };
    void refresh();
    return () => { current = false; controllers.forEach(controller => controller.abort()); if (timer !== undefined) clearTimeout(timer); };
  }, [open, client, agentId, key, retry]);
  const active = jobs.filter(job => job.status === "running" || job.status === "uncertain").length;
  return <section aria-label="Background shell jobs">
    <button type="button" className="header-metric-card subagents-card clickable" onClick={() => setOpen(true)}>
      <span className="header-metric-icon subagents-icon"><i className="fa-solid fa-terminal" aria-hidden="true" /></span>
      <span className="header-metric-center"><span className="header-metric-title">Shell jobs</span></span>
      <span className="header-metric-right"><span className="header-metric-value">{active}</span></span>
    </button>
    <DialogShell open={open} title="Background shell jobs" onClose={() => setOpen(false)}>
      <p>Results are held in memory and expire; they are unavailable after a router restart.</p>
      {jobs.length === 0 ? <p>No retained shell jobs.</p> : <ul className="background-bash-job-list">{jobs.map(job => {
        const saved = details[job.id];
        const detail = saved?.status === job.status ? saved.value : undefined;
        return <li key={job.id}><div>{job.id}: {job.status}</div>
          {detail === "unavailable" ? <p>Output unavailable or expired.</p> : detail === "error" ? <p>Could not load output. <button type="button" onClick={() => { delete cache.current[job.id]; setDetails(previous => { const next = { ...previous }; delete next[job.id]; return next; }); setRetry(n => n + 1); }}>Retry</button></p> : detail ? <>
            {job.status !== "running" && job.status !== "uncertain" && detail.exitCode !== null && <div>Exit code: {detail.exitCode}</div>}
            {detail.truncated && <div>Output truncated at the capture limit.</div>}
            <div className="markdown-body"><pre><code>{detail.output}</code></pre></div>
          </> : <p>Loading output…</p>}
        </li>;
      })}</ul>}
    </DialogShell>
  </section>;
}
