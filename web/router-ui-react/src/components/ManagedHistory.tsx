import { useMemo, useState, type ReactElement } from "react";
import type { ManagedHistoryEntry } from "../protocol/types";

export function ManagedHistory({ quiet, complete }: { quiet: ManagedHistoryEntry[]; complete: ManagedHistoryEntry[] }): ReactElement {
  const [view, setView] = useState<"quiet" | "complete">("quiet");
  const [filter, setFilter] = useState("");
  const entries = view === "quiet" ? quiet : complete;
  const visibleEntries = useMemo(() => { const query = filter.trim().toLocaleLowerCase(); return query === "" ? entries : entries.filter((entry) => `${entry.level ?? ""} ${entry.message ?? ""} ${entry.id}`.toLocaleLowerCase().includes(query)); }, [entries, filter]);
  return <section className="managed-history" aria-label="Managed history"><div className="managed-history-tabs" role="tablist" aria-label="Managed history view"><button type="button" role="tab" aria-selected={view === "quiet"} className={view === "quiet" ? "selected" : "secondary"} onClick={() => setView("quiet")}>Quiet</button><button type="button" role="tab" aria-selected={view === "complete"} className={view === "complete" ? "selected" : "secondary"} onClick={() => setView("complete")}>Complete</button></div><label className="managed-history-filter">Filter history<input value={filter} onChange={(event) => setFilter(event.currentTarget.value)} /></label>{visibleEntries.length === 0 ? <p>{entries.length === 0 ? "No history yet." : "No history matches this filter."}</p> : <ol>{visibleEntries.map((entry) => <li key={entry.id}><strong>{entry.level ?? "event"}</strong>{entry.at ? <time dateTime={entry.at}>{entry.at}</time> : null}<span>{entry.message ?? entry.id}</span></li>)}</ol>}</section>;
}
