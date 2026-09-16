import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactElement } from "react";
import { createHttpClient, HttpRequestError, type RouterLogDiagnostic, type RouterLogQuery, type RouterLogResponse } from "../transport/http-client";
import { isRecord } from "../protocol/guards";
import { DialogShell } from "./DialogShell";

const emptyQuery: RouterLogQuery = { query: "", from: null, to: null, cursor: null };
const secretKeyPattern = /(?:password|passwd|secret|token|access[_-]?token|api[_-]?key|authorization|cookie|credential)/i;
const secretAssignmentPattern = /\b(?:password|passwd|secret|token|access[_-]?token|api[_-]?key|authorization|cookie|credential)\s*[:=]\s*\S+/gi;
const bearerPattern = /\b(?:Bearer|Basic)\s+\S+/gi;

type LogRecord = Record<string, unknown>;
interface DisplayLogField { key: string; value: string; }
interface DisplayLogLine {
  event: string;
  timestamp?: string;
  timestampLabel?: string;
  fields: DisplayLogField[];
  raw: string;
}

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
function diagnosticLabel(diagnostic: RouterLogDiagnostic): string { return `${diagnostic.category.replaceAll("_", " ")} (${diagnostic.count})`; }
function timestampInput(value: string): string | null {
  if (value === "") return null;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  const parsed = new Date(value);
  if (match === null || Number.isNaN(parsed.getTime()) || parsed.getFullYear() !== Number(match[1]) || parsed.getMonth() + 1 !== Number(match[2]) || parsed.getDate() !== Number(match[3]) || parsed.getHours() !== Number(match[4]) || parsed.getMinutes() !== Number(match[5])) return value;
  return parsed.toISOString();
}
function asRecord(value: unknown): LogRecord | undefined { return isRecord(value) ? value : undefined; }
function redactText(value: string, knownSecrets: readonly string[]): string {
  let result = value;
  for (const secret of knownSecrets.filter((candidate) => candidate !== "").sort((a, b) => b.length - a.length)) result = result.split(secret).join("[REDACTED]");
  return result.replace(bearerPattern, "[REDACTED]").replace(secretAssignmentPattern, (match) => match.replace(/([:=])\s*\S+$/, "$1 [REDACTED]"));
}
function redactValue(value: unknown, key: string | undefined, knownSecrets: readonly string[]): unknown {
  if (key !== undefined && secretKeyPattern.test(key)) return "[REDACTED]";
  if (typeof value === "string") return redactText(value, knownSecrets);
  if (Array.isArray(value)) return value.map((item) => redactValue(item, undefined, knownSecrets));
  const record = asRecord(value);
  if (record === undefined) return value;
  return Object.fromEntries(Object.entries(record).map(([childKey, childValue]) => [childKey, redactValue(childValue, childKey, knownSecrets)]));
}
function displayValue(value: unknown): string {
  if (typeof value === "string") return value;
  const encoded = JSON.stringify(value);
  return encoded === undefined ? String(value) : encoded;
}
function humanEvent(value: unknown): string {
  const text = typeof value === "string" ? value.replace(/[^a-zA-Z0-9 _-]/g, "").replaceAll("_", " ").trim() : "";
  if (text === "") return "Router event";
  return text.charAt(0).toUpperCase() + text.slice(1);
}
function readJsonString(value: string, start: number): { end: number; value: string } | undefined {
  if (value[start] !== '"') return undefined;
  let escaped = false;
  for (let index = start + 1; index < value.length; index += 1) {
    const character = value[index];
    if (escaped) { escaped = false; continue; }
    if (character === "\\") { escaped = true; continue; }
    if (character === '"') {
      try {
        const decoded: unknown = JSON.parse(value.slice(start, index + 1));
        return typeof decoded === "string" ? { end: index + 1, value: decoded } : undefined;
      } catch { return undefined; }
    }
  }
  return undefined;
}
function jsonValueEnd(value: string, start: number): number {
  const first = value[start];
  if (first === '"') return readJsonString(value, start)?.end ?? start;
  if (first !== "{" && first !== "[") {
    let end = start;
    while (end < value.length && !",}]\r\n".includes(value[end] ?? "")) end += 1;
    return end;
  }
  const opening = first;
  const closing = opening === "{" ? "}" : "]";
  let depth = 0;
  let index = start;
  while (index < value.length) {
    if (value[index] === '"') { index = readJsonString(value, index)?.end ?? value.length; continue; }
    if (value[index] === opening) depth += 1;
    if (value[index] === closing) { depth -= 1; if (depth === 0) return index + 1; }
    index += 1;
  }
  return value.length;
}
function redactRawLine(value: string, knownSecrets: readonly string[]): string {
  let output = "";
  let index = 0;
  while (index < value.length) {
    const key = value[index] === '"' ? readJsonString(value, index) : undefined;
    if (key !== undefined) {
      let cursor = key.end;
      while (cursor < value.length && " \t".includes(value[cursor] ?? "")) cursor += 1;
      if (value[cursor] === ":" && secretKeyPattern.test(key.value)) {
        cursor += 1;
        while (cursor < value.length && " \t".includes(value[cursor] ?? "")) cursor += 1;
        const end = jsonValueEnd(value, cursor);
        output += value.slice(index, cursor) + JSON.stringify("[REDACTED]");
        index = end;
        continue;
      }
      output += value.slice(index, key.end);
      index = key.end;
      continue;
    }
    output += value[index] ?? "";
    index += 1;
  }
  return redactText(output, knownSecrets);
}
function displayLogLine(encoded: string, agentId: string | undefined, search: string, knownSecrets: readonly string[]): DisplayLogLine | undefined {
  const decoded = decodeRouterLogBytes(encoded);
  const matchesSearch = search.trim() === "" || decoded.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase());
  let parsed: unknown;
  try { parsed = JSON.parse(decoded.trim()); } catch {
    if (agentId === undefined || !decoded.includes(agentId) || !matchesSearch) return undefined;
    return { event: "Unstructured log entry", fields: [{ key: "raw", value: redactText(decoded, knownSecrets) }], raw: redactRawLine(decoded, knownSecrets) };
  }
  const record = asRecord(parsed);
  if (record === undefined || agentId !== undefined && record.agent_id !== agentId || !matchesSearch) return undefined;
  const redacted = redactValue(record, undefined, knownSecrets);
  const redactedRecord = asRecord(redacted);
  if (redactedRecord === undefined) return undefined;
  const rawTimestamp = [record.timestamp, record.created_at, record.time].find((value): value is string => typeof value === "string");
  let timestamp: string | undefined;
  let timestampLabel: string | undefined;
  if (rawTimestamp !== undefined) {
    const parsed = new Date(rawTimestamp);
    if (!Number.isNaN(parsed.getTime())) { timestamp = parsed.toISOString(); timestampLabel = parsed.toLocaleString([], { dateStyle: "medium", timeStyle: "medium" }); }
  }
  const fields = Object.entries(redactedRecord).map(([key, value]) => ({ key, value: displayValue(value) }));
  const timestampDetails = timestamp !== undefined && timestampLabel !== undefined ? { timestamp, timestampLabel } : {};
  return { event: humanEvent(record.event ?? record.type ?? record.status), ...timestampDetails, fields, raw: redactRawLine(decoded, knownSecrets) };
}

export interface RouterLogViewerProps {
  routerUrl: string;
  agentId?: string | undefined;
  open?: boolean | undefined;
  onClose?: (() => void) | undefined;
  onClosed?: (() => void) | undefined;
  knownSecrets?: readonly string[] | undefined;
}

export function RouterLogViewer({ routerUrl, agentId, open = true, onClose = () => undefined, onClosed, knownSecrets = [] }: RouterLogViewerProps): ReactElement {
  const client = useMemo(() => createHttpClient(routerUrl), [routerUrl]);
  const [query, setQuery] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [view, setView] = useState<"structured" | "raw">("structured");
  const [pageState, setPageState] = useState<{ pages: RouterLogResponse[]; pageIndex: number }>({ pages: [], pageIndex: 0 });
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const requestGeneration = useRef(0);
  const { pages, pageIndex } = pageState;
  const current = pages[pageIndex];
  const requestQuery = query.trim() === "" ? agentId ?? "" : query;

  const invalidateRequests = (): void => { requestGeneration.current += 1; setLoading(false); };
  const load = useCallback(async (request: RouterLogQuery): Promise<void> => {
    const generation = ++requestGeneration.current;
    setLoading(true); setError(undefined);
    try {
      const response = await client.routerLog(request);
      if (requestGeneration.current === generation) setPageState({ pages: [response], pageIndex: 0 });
    } catch (cause) {
      if (requestGeneration.current === generation) { setError(errorMessage(cause)); setPageState({ pages: [], pageIndex: 0 }); }
    } finally { if (requestGeneration.current === generation) setLoading(false); }
  }, [client]);
  useEffect(() => { setQuery(""); setFrom(""); setTo(""); void load({ ...emptyQuery, query: agentId ?? "" }); }, [agentId, load]);
  const submit = (event: FormEvent<HTMLFormElement>): void => { event.preventDefault(); void load({ query: requestQuery, from: timestampInput(from), to: timestampInput(to), cursor: null }); };
  const refresh = (): void => { void load({ query: requestQuery, from: timestampInput(from), to: timestampInput(to), cursor: null }); };
  const older = (): void => {
    if (!current?.next_cursor || loading) return;
    const generation = ++requestGeneration.current;
    setLoading(true); setError(undefined);
    const request: RouterLogQuery = { query: requestQuery, from: timestampInput(from), to: timestampInput(to), cursor: current.next_cursor };
    void client.routerLog(request).then((response) => {
      if (requestGeneration.current === generation) setPageState((state) => ({ pages: [...state.pages.slice(0, pageIndex + 1), response], pageIndex: pageIndex + 1 }));
    }).catch(async (cause) => {
      if (requestGeneration.current !== generation) return;
      if (cause instanceof HttpRequestError && cause.code === "invalid_cursor") {
        setPageState({ pages: [], pageIndex: 0 });
        await load({ ...request, cursor: null });
      } else setError(errorMessage(cause));
    }).finally(() => { if (requestGeneration.current === generation) setLoading(false); });
  };
  const clear = (): void => { invalidateRequests(); setQuery(""); setFrom(""); setTo(""); void load({ ...emptyQuery, query: agentId ?? "" }); };
  const displayLines = current?.lines.flatMap((line) => {
    try { const display = displayLogLine(line.bytes_base64, agentId, query, knownSecrets); return display === undefined ? [] : [display]; } catch { return []; }
  }) ?? [];

  return <DialogShell open={open} title={agentId ? `Logs for ${agentId}` : "Router logs"} description={agentId ? `Showing router events for ${agentId}.` : "Showing router events."} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close logs" className="message-dialog router-log-dialog">
    <div className="router-log-content">
      <form className="router-log-controls" onSubmit={submit}><label>Search<input type="search" value={query} onChange={(event) => { invalidateRequests(); setQuery(event.currentTarget.value); }} /></label><label>From<input type="datetime-local" value={from} onChange={(event) => { invalidateRequests(); setFrom(event.currentTarget.value); }} /></label><label>To<input type="datetime-local" value={to} onChange={(event) => { invalidateRequests(); setTo(event.currentTarget.value); }} /></label><button type="submit" disabled={loading}>Search</button><button type="button" onClick={refresh} disabled={loading}>Refresh</button><button type="button" onClick={clear} disabled={loading}>Clear</button></form>
      <p className="router-log-read-only">Credentials and access tokens are redacted.</p>
      <div className="router-log-view-toggle" role="group" aria-label="Log view"><button type="button" className={view === "structured" ? "selected" : "secondary"} aria-pressed={view === "structured"} onClick={() => setView("structured")}>Readable</button><button type="button" className={view === "raw" ? "selected" : "secondary"} aria-pressed={view === "raw"} onClick={() => setView("raw")}>Raw</button></div>
      {loading ? <p role="status">Loading router logs…</p> : null}{error ? <p className="error" role="alert">{error}</p> : null}{current?.status === "source_unavailable" ? <p role="alert">Router log source unavailable. No records were loaded.</p> : null}{current?.status === "partial" ? <p role="status">Router log results are partial. Some records could not be read.</p> : null}{!loading && !error && current?.status === "complete" && displayLines.length === 0 ? <p role="status">No matching router log records.</p> : null}{current && current.diagnostics.length > 0 ? <aside aria-label="Router log diagnostics"><strong>Diagnostics</strong><ul>{current.diagnostics.map((diagnostic) => <li key={diagnostic.category}>{diagnosticLabel(diagnostic)}</li>)}</ul></aside> : null}
      <section className={`router-log-panel ${view === "raw" ? "raw" : "structured"}`} aria-label="Router log records">{displayLines.map((line, index) => <article className="router-log-line" key={`${pageIndex}-${index}`}><div className="router-log-line-heading">{line.timestamp ? <time dateTime={line.timestamp}>{line.timestampLabel}</time> : null}<strong>{line.event}</strong></div>{view === "raw" ? <pre>{line.raw}</pre> : <dl>{line.fields.map((field) => <div key={field.key}><dt>{field.key}</dt><dd>{field.value}</dd></div>)}</dl>}</article>)}</section>
      <nav className="router-log-paging" aria-label="Router log pages"><button type="button" onClick={() => setPageState((state) => ({ ...state, pageIndex: state.pageIndex - 1 }))} disabled={pageIndex === 0 || loading}>Newer</button><button type="button" onClick={older} disabled={!current?.next_cursor || loading}>Older</button></nav>
    </div>
  </DialogShell>;
}
