import { useCallback, useEffect, useRef, useState, type ReactElement } from "react";
import type { RouterHttpClient, ScratchpadEntry } from "../transport/http-client";
import { humanName } from "../state/selectors";
import { DialogShell } from "./DialogShell";
import { MarkdownBody } from "../domain/markdown";

export interface ScratchpadViewerProps {
  client: RouterHttpClient;
  agentId: string;
  open?: boolean | undefined;
  onClose?: (() => void) | undefined;
  onClosed?: (() => void) | undefined;
}

function formatTimestamp(value: string | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

export function ScratchpadViewer({
  client,
  agentId,
  open = true,
  onClose = () => {},
  onClosed,
}: ScratchpadViewerProps): ReactElement {
  const [entries, setEntries] = useState<ScratchpadEntry[]>([]);
  const [totalCount, setTotalCount] = useState<number>(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const requestIdRef = useRef(0);

  const loadEntries = useCallback(async (): Promise<void> => {
    const currentRequestId = ++requestIdRef.current;
    setLoading(true);
    setError(undefined);
    try {
      const resp = await client.scratchpadForAgent(agentId);
      if (requestIdRef.current === currentRequestId) {
        // Pinned entries sorted/kept at top, unpinned follow
        const pinned = resp.entries.filter((e) => e.pinned);
        const unpinned = resp.entries.filter((e) => !e.pinned);
        setEntries([...pinned, ...unpinned]);
        setTotalCount(resp.total_count);
      }
    } catch (cause) {
      if (requestIdRef.current === currentRequestId) {
        setError(cause instanceof Error ? cause.message : "Scratchpad could not be loaded.");
        setEntries([]);
        setTotalCount(0);
      }
    } finally {
      if (requestIdRef.current === currentRequestId) {
        setLoading(false);
      }
    }
  }, [agentId, client]);

  useEffect(() => {
    if (open) {
      void loadEntries();
    }
  }, [open, loadEntries]);

  const name = humanName(agentId);

  return (
    <DialogShell
      open={open}
      title={`Scratchpad for ${name}`}
      description={`Persistent notes and working context for ${name}.`}
      onClose={onClose}
      {...(onClosed === undefined ? {} : { onClosed })}
      closeLabel="Close scratchpad"
      className="message-dialog scratchpad-dialog"
    >
      <div className="scratchpad-content">
        <div className="scratchpad-controls" style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "1rem" }}>
          <span style={{ fontSize: "0.85em", color: "var(--text-muted, #888)" }}>
            {totalCount > 0 ? `${totalCount} ${totalCount === 1 ? "entry" : "entries"}` : ""}
          </span>
          <button
            type="button"
            className="action-button scratchpad-refresh-button"
            onClick={() => void loadEntries()}
            disabled={loading}
            aria-label="Refresh scratchpad"
            title="Refresh"
          >
            <i className={`fa-solid fa-arrows-rotate ${loading ? "fa-spin" : ""}`} aria-hidden="true" />
          </button>
        </div>

        {loading && entries.length === 0 ? (
          <p className="scratchpad-status" role="status">Loading scratchpad entries…</p>
        ) : null}

        {error ? (
          <p className="scratchpad-error error" role="alert">{error}</p>
        ) : null}

        {!loading && !error && entries.length === 0 ? (
          <p className="scratchpad-empty" role="status">No scratchpad entries recorded for this agent.</p>
        ) : null}

        {entries.length > 0 ? (
          <div className="scratchpad-entries-list" style={{ display: "flex", flexDirection: "column", gap: "0.75rem", maxHeight: "60vh", overflowY: "auto" }}>
            {entries.map((entry) => (
              <article
                key={entry.id}
                className={`scratchpad-entry-card ${entry.pinned ? "pinned" : ""}`}
                style={{
                  border: entry.pinned ? "1px solid var(--accent-color, #f59e0b)" : "1px solid var(--border-color, #333)",
                  borderRadius: 6,
                  padding: "0.75rem",
                  background: entry.pinned ? "rgba(245, 158, 11, 0.05)" : "var(--bg-secondary, #242424)",
                }}
              >
                <div
                  className="scratchpad-entry-header"
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    marginBottom: "0.5rem",
                    fontSize: "0.8em",
                    color: "var(--text-muted, #888)",
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                    {entry.pinned ? (
                      <span
                        className="scratchpad-pin-badge"
                        style={{
                          fontWeight: 600,
                          color: "var(--accent-color, #f59e0b)",
                          background: "rgba(245, 158, 11, 0.15)",
                          padding: "2px 6px",
                          borderRadius: 4,
                          fontSize: "0.85em",
                        }}
                      >
                        📌 Pinned
                      </span>
                    ) : null}
                    <time dateTime={entry.created_at}>{formatTimestamp(entry.created_at)}</time>
                  </div>
                </div>
                <div
                  className="scratchpad-entry-body"
                  style={{
                    whiteSpace: "pre-wrap",
                    wordBreak: "break-word",
                    fontSize: "0.95em",
                    lineHeight: "1.4",
                  }}
                >
                  <MarkdownBody body={entry.content} />
                </div>
              </article>
            ))}
          </div>
        ) : null}
      </div>
    </DialogShell>
  );
}
