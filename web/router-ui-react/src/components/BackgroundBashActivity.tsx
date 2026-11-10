import { useState } from "react";
import type { BackgroundBashJob } from "../protocol/types";
import { DialogShell } from "./DialogShell";

export function BackgroundBashActivity({ jobs }: { jobs: BackgroundBashJob[] | undefined }) {
  if (!jobs?.length) return null;
  return <BackgroundBashActivityWithJobs jobs={jobs} />;
}

function BackgroundBashActivityWithJobs({ jobs }: { jobs: BackgroundBashJob[] }) {
  const [open, setOpen] = useState(false);
  return <section aria-label="Background shell jobs">
    <button type="button" className="header-metric-card subagents-card clickable" onClick={() => setOpen(true)}>
      <span className="header-metric-icon subagents-icon"><i className="fa-solid fa-terminal" aria-hidden="true" /></span>
      <span className="header-metric-center"><span className="header-metric-title">Shell jobs</span></span>
      <span className="header-metric-right"><span className="header-metric-value">{jobs.length}</span></span>
    </button>
    <DialogShell open={open} title="Background shell jobs" onClose={() => setOpen(false)}>
      <ul className="background-bash-job-list">{jobs.map((job) => <li key={job.id}>{job.id}: {job.status}</li>)}</ul>
    </DialogShell>
  </section>;
}
