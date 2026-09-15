import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactElement } from "react";
import { createHttpClient, type RouterLogQuery, type RouterLogResponse } from "../transport/http-client";

const emptyQuery: RouterLogQuery = { query: "", from: null, to: null, cursor: null };

export function decodeRouterLogBytes(value: string): string {
  const binary = atob(value);
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let output = "";
  let index = 0;
  while (index < bytes.length) {
    let decoded = false;
    for (let width = Math.min(4, bytes.length - index); width >= 1; width -= 1) {
      try { output += decoder.decode(bytes.slice(index, index + width)); index += width; decoded = true; break; } catch { /* try a shorter valid UTF-8 sequence */ }
    }
    if (!decoded) { const byte = bytes[index]; if (byte !== undefined) output += `\\x${byte.toString(16).padStart(2, "0")}`; index += 1; }
  }
  return output;
}

function errorMessage(cause: unknown): string { return cause instanceof Error ? cause.message : "Router logs could not be loaded."; }
function timestampInput(value: string): string | null { if (value === "") return null; const parsed = new Date(value); return Number.isNaN(parsed.getTime()) ? value : parsed.toISOString(); }

export function RouterLogViewer({ routerUrl }: { routerUrl: string }): ReactElement {
  const client = useMemo(() => createHttpClient(routerUrl), [routerUrl]);
  const [query, setQuery] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [pages, setPages] = useState<RouterLogResponse[]>([]);
  const [pageIndex, setPageIndex] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const current = pages[pageIndex];

  const load = useCallback(async (request: RouterLogQuery): Promise<void> => {
    setLoading(true); setError(undefined);
    try { const response = await client.routerLog(request); setPages([response]); setPageIndex(0); }
    catch (cause) { setError(errorMessage(cause)); setPages([]); setPageIndex(0); }
    finally { setLoading(false); }
  }, [client]);
  useEffect(() => { void load(emptyQuery); }, [load]);
  const submit = (event: FormEvent<HTMLFormElement>): void => { event.preventDefault(); void load({ query, from: timestampInput(from), to: timestampInput(to), cursor: null }); };
  const older = (): void => {
    if (!current?.next_cursor || loading) return;
    setLoading(true); setError(undefined);
    void client.routerLog({ query, from: timestampInput(from), to: timestampInput(to), cursor: current.next_cursor }).then((response) => { setPages((existing) => [...existing.slice(0, pageIndex + 1), response]); setPageIndex((index) => index + 1); }).catch((cause) => setError(errorMessage(cause))).finally(() => setLoading(false));
  };
  const clear = (): void => { setQuery(""); setFrom(""); setTo(""); void load(emptyQuery); };
  return <main className="router-log-page">
    <header className="router-log-header"><div><h1>Router logs</h1><p>Retained router events, newest results first.</p></div><button type="button" onClick={() => { window.location.pathname = "/"; }}>Back to mailbox</button></header>
    <form className="router-log-controls" onSubmit={submit}><label>Search<input type="search" value={query} onChange={(event) => setQuery(event.currentTarget.value)} /></label><label>From<input type="datetime-local" value={from} onChange={(event) => setFrom(event.currentTarget.value)} /></label><label>To<input type="datetime-local" value={to} onChange={(event) => setTo(event.currentTarget.value)} /></label><button type="submit" disabled={loading}>Search</button><button type="button" onClick={() => void load({ query, from: timestampInput(from), to: timestampInput(to), cursor: null })} disabled={loading}>Refresh</button><button type="button" onClick={clear} disabled={loading}>Clear</button></form>
    <p className="router-log-read-only">Read-only. Log records are shown as stored.</p>
    {loading ? <p role="status">Loading router logs…</p> : null}{error ? <p className="error" role="alert">{error}</p> : null}{!loading && !error && current?.lines.length === 0 ? <p role="status">No matching router log records.</p> : null}
    <section className="router-log-panel" aria-label="Router log records">{current?.lines.map((line, index) => <div className="router-log-line" key={`${pageIndex}-${index}`}>{decodeRouterLogBytes(line.bytes_base64)}</div>)}</section>
    <nav className="router-log-paging" aria-label="Router log pages"><button type="button" onClick={() => setPageIndex((index) => index - 1)} disabled={pageIndex === 0 || loading}>Newer</button><button type="button" onClick={older} disabled={!current?.next_cursor || loading}>Older</button></nav>
  </main>;
}
