import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { createHttpClient, type RouterLogDiagnostic, type RouterLogResponse } from "../transport/http-client";
import { isRecord } from "../protocol/guards";
import { DialogShell } from "./DialogShell";

const DISPLAY_KEYS_STORAGE = "mecha.web.routerLogs.v1.displayKeys";
export const DEFAULT_DISPLAY_KEYS = ["timestamp", "time", "created_at", "level", "event", "type", "status", "message", "body", "error"] as const;
const secretKeyPattern = /(?:password|passwd|secret|token|access[_-]?token|api[_-]?key|authorization|cookie|credential)/i;
const secretAssignmentPattern = /"?(?:password|passwd|secret|token|access[_-]?token|api[_-]?key|authorization|cookie|credential)"?\s*[:=]\s*(?:"(?:\\.|[^"\\])*"|[^\s,}\]]+)/gi;
const bearerPattern = /\b(?:Bearer|Basic)\s+\S+/gi;
type LogRecord = Record<string, unknown>;
interface DisplayLogField { key: string; value: string; }
interface DisplayLogLine { event: string; timestamp?: string; timestampLabel?: string; fields: DisplayLogField[]; }

export interface DisplayKeyStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
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
      try {
        output += decoder.decode(bytes.slice(index, index + width));
        index += width;
        decoded = true;
        break;
      } catch { /* Render each invalid byte without dropping the record. */ }
    }
    if (!decoded) {
      const byte = bytes[index];
      if (byte !== undefined) output += `\\x${byte.toString(16).padStart(2, "0")}`;
      index += 1;
    }
  }
  return output;
}

function errorMessage(cause: unknown): string { return cause instanceof Error ? cause.message : "Router logs could not be loaded."; }
function diagnosticLabel(diagnostic: RouterLogDiagnostic): string { return `${diagnostic.category.replaceAll("_", " ")} (${diagnostic.count})`; }
function asRecord(value: unknown): LogRecord | undefined { return isRecord(value) ? value : undefined; }
function redactText(value: string, knownSecrets: readonly string[]): string {
  let result = value;
  for (const secret of knownSecrets.filter((candidate) => candidate !== "").sort((a, b) => b.length - a.length)) result = result.split(secret).join("[REDACTED]");
  result = result.replace(bearerPattern, "[REDACTED]");
  return result.replace(secretAssignmentPattern, (match) => {
    const separator = Math.max(match.lastIndexOf(":"), match.lastIndexOf("="));
    return separator < 0 ? "[REDACTED]" : `${match.slice(0, separator + 1)} [REDACTED]`;
  });
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
function selectedTimestamp(record: LogRecord, selectedKeys: readonly string[]): [string, string] | undefined {
  for (const key of ["timestamp", "created_at", "time"]) {
    if (!selectedKeys.includes(key) || typeof record[key] !== "string") continue;
    const parsed = new Date(record[key]);
    if (!Number.isNaN(parsed.getTime())) return [parsed.toISOString(), parsed.toLocaleString([], { dateStyle: "medium", timeStyle: "medium" })];
  }
  return undefined;
}
function displayLogLine(encoded: string, agentId: string, search: string, selectedKeys: readonly string[], knownSecrets: readonly string[]): DisplayLogLine | undefined {
  const decoded = decodeRouterLogBytes(encoded);
  if (search !== "" && !decoded.toLocaleLowerCase().includes(search.toLocaleLowerCase())) return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(decoded.trim());
  } catch {
    return decoded.includes(agentId) ? { event: "Unstructured log entry", fields: [{ key: "record", value: redactText(decoded, knownSecrets) }] } : undefined;
  }
  const record = asRecord(parsed);
  if (record === undefined || record.agent_id !== agentId) return undefined;
  const redactedRecord = asRecord(redactValue(record, undefined, knownSecrets));
  if (redactedRecord === undefined) return { event: "Unstructured log entry", fields: [{ key: "record", value: redactText(decoded, knownSecrets) }] };
  const eventKey = ["event", "type", "status"].find((key) => selectedKeys.includes(key) && record[key] !== undefined);
  const time = selectedTimestamp(record, selectedKeys);
  const fields = Object.entries(redactedRecord)
    .filter(([key]) => selectedKeys.includes(key))
    .map(([key, value]) => ({ key, value: displayValue(value) }));
  return { event: eventKey === undefined ? "Router event" : humanEvent(record[eventKey]), ...(time === undefined ? {} : { timestamp: time[0], timestampLabel: time[1] }), fields };
}

function storage(): DisplayKeyStorage | undefined {
  try { return typeof globalThis.localStorage === "undefined" ? undefined : globalThis.localStorage; } catch { return undefined; }
}

export function loadDisplayKeys(target: Pick<DisplayKeyStorage, "getItem"> | undefined = storage()): string[] {
  try {
    const value = target?.getItem(DISPLAY_KEYS_STORAGE);
    if (value === null || value === undefined) return [...DEFAULT_DISPLAY_KEYS];
    const parsed: unknown = JSON.parse(value);
    if (!Array.isArray(parsed)) return [...DEFAULT_DISPLAY_KEYS];
    const keys: string[] = [];
    for (const key of parsed) {
      if (typeof key !== "string" || key.trim() === "") return [...DEFAULT_DISPLAY_KEYS];
      keys.push(key);
    }
    return [...new Set(keys)];
  } catch { return [...DEFAULT_DISPLAY_KEYS]; }
}

export function saveDisplayKeys(keys: readonly string[], target: Pick<DisplayKeyStorage, "setItem"> | undefined = storage()): void {
  try { target?.setItem(DISPLAY_KEYS_STORAGE, JSON.stringify([...new Set(keys)])); } catch { /* Storage can be unavailable or full. */ }
}

export function useDebouncedValue<T>(value: T, delayMs = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return debounced;
}

export interface RouterLogViewerProps {
  routerUrl: string;
  agentId: string;
  open?: boolean | undefined;
  onClose?: (() => void) | undefined;
  onClosed?: (() => void) | undefined;
  knownSecrets?: readonly string[] | undefined;
  storage?: DisplayKeyStorage | undefined;
}

export function RouterLogViewer({ routerUrl, agentId, open = true, onClose = () => undefined, onClosed, knownSecrets = [], storage: displayKeyStorage }: RouterLogViewerProps): ReactElement {
  const client = useMemo(() => createHttpClient(routerUrl), [routerUrl]);
  const [query, setQuery] = useState("");
  const debouncedQuery = useDebouncedValue(query);
  const [displayKeys, setDisplayKeys] = useState<string[]>(() => loadDisplayKeys(displayKeyStorage));
  const [page, setPage] = useState<RouterLogResponse>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string>();
  const requestGeneration = useRef(0);
  const load = useCallback(async (): Promise<void> => {
    const generation = ++requestGeneration.current;
    setLoading(true);
    setError(undefined);
    try {
      const response = await client.routerLogForAgent(agentId, { query: "", from: null, to: null, cursor: null });
      if (requestGeneration.current === generation) setPage(response);
    } catch (cause) {
      if (requestGeneration.current === generation) { setError(errorMessage(cause)); setPage(undefined); }
    } finally {
      if (requestGeneration.current === generation) setLoading(false);
    }
  }, [agentId, client]);

  useEffect(() => { setQuery(""); setPage(undefined); }, [agentId]);
  useEffect(() => { void load(); }, [load]);

  const availableKeys = useMemo(() => {
    const keys = new Set<string>(DEFAULT_DISPLAY_KEYS);
    for (const line of page?.lines ?? []) {
      try {
        const record = asRecord(JSON.parse(decodeRouterLogBytes(line.bytes_base64).trim()));
        if (record !== undefined) Object.keys(record).forEach((key) => keys.add(key));
      } catch { /* The fallback record does not provide selectable JSON fields. */ }
    }
    return [...keys].sort((a, b) => a.localeCompare(b));
  }, [page]);
  const setKeyEnabled = (key: string, enabled: boolean): void => {
    const next = enabled ? [...displayKeys, key] : displayKeys.filter((candidate) => candidate !== key);
    setDisplayKeys(next);
    saveDisplayKeys(next, displayKeyStorage);
  };
  const displayLines = page?.lines.flatMap((line) => {
    try {
      const display = displayLogLine(line.bytes_base64, agentId, debouncedQuery, displayKeys, knownSecrets);
      return display === undefined ? [] : [display];
    } catch { return []; }
  }) ?? [];

  return <DialogShell open={open} title={`Logs for ${agentId}`} description={`Showing router events for ${agentId}.`} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close logs" className="message-dialog router-log-dialog">
    <div className="router-log-content">
      <div className="router-log-controls">
        <label>Filter<input type="search" value={query} onChange={(event) => setQuery(event.currentTarget.value)} /></label>
        <button className="router-log-refresh" type="button" onClick={() => void load()} disabled={loading} aria-label="Refresh" title="Refresh"><i className="fa-solid fa-arrows-rotate" aria-hidden="true" /></button>
      </div>
      <section className="router-log-fields" aria-labelledby="router-log-fields-heading">
        <h3 id="router-log-fields-heading">Displayed fields</h3>
        <fieldset aria-labelledby="router-log-fields-heading"><legend className="router-log-fields-legend">Field selection</legend>{availableKeys.map((key) => <label key={key}><input type="checkbox" checked={displayKeys.includes(key)} onChange={(event) => setKeyEnabled(key, event.currentTarget.checked)} />{key}</label>)}</fieldset>
      </section>
      {loading ? <p role="status">Loading router logs…</p> : null}
      {error ? <p className="error" role="alert">{error}</p> : null}
      {page?.status === "source_unavailable" ? <p role="alert">Router log source unavailable. No records were loaded.</p> : null}
      {page?.status === "partial" ? <p role="status">Router log results are partial. Some records could not be read.</p> : null}
      {!loading && !error && page?.status === "complete" && displayLines.length === 0 ? <p role="status">No matching router log records.</p> : null}
      {page && page.diagnostics.length > 0 ? <aside aria-label="Router log diagnostics"><strong>Diagnostics</strong><ul>{page.diagnostics.map((diagnostic) => <li key={diagnostic.category}>{diagnosticLabel(diagnostic)}</li>)}</ul></aside> : null}
      <section className="router-log-panel structured" aria-label="Router log records">{displayLines.map((line, index) => <article className="router-log-line" key={index}><div className="router-log-line-heading">{line.timestamp ? <time dateTime={line.timestamp}>{line.timestampLabel}</time> : null}<strong>{line.event}</strong></div><dl>{line.fields.map((field) => <div key={field.key}><dt>{field.key}</dt><dd>{field.value}</dd></div>)}</dl></article>)}</section>
    </div>
  </DialogShell>;
}
