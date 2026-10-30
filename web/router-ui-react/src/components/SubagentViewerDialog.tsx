import { useEffect, useLayoutEffect, useMemo, useRef, useState, useId, type ReactElement, type MouseEvent } from "react";
import type { ManagedChild, ManagedHistoryEntry } from "../protocol/types";
import type { SubagentSummary } from "../state/agent-projection";
import { compareManagedHistoryEntries } from "../state/reducer";
import { DEFAULT_AUTO_SCROLL_THRESHOLD, shouldFollowToBottom } from "../state/conversation-projection";
import type { ManagedHistoryResponse, RouterHttpClient } from "../transport/http-client";
import { MarkdownBody } from "../domain/markdown";
import { CompleteTimelineEntry, getThinkingContent, isContextResetEntry, ThinkingTraceCard } from "./CompleteTimelineEntry";
import { DialogShell } from "./DialogShell";

export type Client = Pick<RouterHttpClient, "managedHistory">;

export type SubagentViewerItem = (SubagentSummary & Partial<ManagedChild>) | ManagedChild;

export interface SubagentViewerDialogProps {
  open: boolean;
  onClose: () => void;
  agentId?: string | undefined;
  subagents?: (ManagedChild | SubagentSummary)[] | undefined;
  client?: Client | undefined;
  onStopSubagent?: ((subagentId: string) => Promise<void> | void) | undefined;
}

function formatDuration(startedAt?: string, endedAt?: string, durationMs?: number, nowMs?: number): string | null {
  if (typeof durationMs === "number" && Number.isFinite(durationMs) && durationMs > 0) {
    const totalSeconds = Math.floor(durationMs / 1000);
    if (totalSeconds < 60) {
      return `${totalSeconds}s`;
    }
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return `${minutes}m ${seconds}s`;
  }

  if (startedAt) {
    const startTime = Date.parse(startedAt);
    if (Number.isFinite(startTime)) {
      const endTime = endedAt ? Date.parse(endedAt) : (nowMs ?? Date.now());
      if (Number.isFinite(endTime) && endTime >= startTime) {
        const diffSec = Math.floor((endTime - startTime) / 1000);
        if (diffSec < 60) return `${diffSec}s`;
        const minutes = Math.floor(diffSec / 60);
        const seconds = diffSec % 60;
        return `${minutes}m ${seconds}s`;
      }
    }
  }

  return null;
}

function formatTimestamp(timestamp?: string): string | null {
  if (!timestamp) return null;
  const parsed = Date.parse(timestamp);
  if (!Number.isFinite(parsed)) return null;
  return new Date(parsed).toLocaleTimeString([], { hour: "numeric", minute: "2-digit", second: "2-digit" });
}

function getSubagentTime(s: ManagedChild | SubagentSummary): number {
  const item = s as Partial<ManagedChild> & { created_at?: string };
  const ts = item.started_at || item.created_at;
  if (!ts) return 0;
  const time = new Date(ts).getTime();
  return Number.isFinite(time) ? time : 0;
}

function isToolOrActivityMeta(entry: ManagedHistoryEntry): boolean {
  if (isContextResetEntry(entry)) return true;
  const kind = entry.presentation?.kind;
  if (
    kind === "tool" ||
    kind === "thinking" ||
    kind === "compaction" ||
    kind === "goal_evaluation" ||
    kind === "child" ||
    kind === "subagent" ||
    kind === "subagent_complete" ||
    kind === "recovery"
  ) {
    return true;
  }
  const category = entry.category;
  if (
    category === "managed_tool" ||
    category === "managed_builtin_tool" ||
    category === "managed_thinking" ||
    category === "managed_goal_evaluation" ||
    category === "managed_parent_compaction" ||
    category === "managed_child" ||
    category === "managed_subagent"
  ) {
    return true;
  }
  return false;
}

export function formatLastActivityPreview(entry?: ManagedHistoryEntry): string | null {
  if (!entry) return null;
  const toolName = entry.presentation?.tool_name;
  if (entry.presentation?.kind === "tool" || entry.category === "managed_tool") {
    if (toolName === "bash") {
      const cmd = (entry.tool_input as { command?: string } | undefined)?.command || entry.presentation?.detail || "";
      return cmd.trim() || "bash";
    }
    if (toolName === "read" || toolName === "edit" || toolName === "write") {
      const p = (entry.tool_input as { path?: string } | undefined)?.path || entry.presentation?.detail || "";
      return p ? `${toolName}: ${p}` : toolName;
    }
    if (entry.presentation?.detail) {
      return entry.presentation.detail;
    }
    return toolName || "Tool invocation";
  }
  if (entry.presentation?.summary) {
    return entry.presentation.summary;
  }
  if (entry.message_content) {
    return entry.message_content.trim();
  }
  if (entry.message && entry.message !== "Message sent" && entry.message !== "Reply sent") {
    return entry.message.trim();
  }
  return null;
}

function getMessageEntryDetails(
  entry: ManagedHistoryEntry,
): { role: string; text: string; isAssistant: boolean } | null {
  if (isToolOrActivityMeta(entry)) {
    return null;
  }

  const isExplicitAssistant = entry.category === "assistant_text";

  const rawText =
    entry.message_content ??
    (entry.presentation?.kind === "message" ? entry.presentation?.summary : undefined) ??
    (isExplicitAssistant ? entry.message : undefined) ??
    entry.message;
  const text = rawText?.trim();

  if (!text || text === "(Action completed without additional text)") {
    return null;
  }

  if (
    (text === "Message sent" || text === "Reply sent" || text === "Message acknowledged") &&
    !entry.message_content
  ) {
    return null;
  }

  const isUser =
    !isExplicitAssistant &&
    (entry.category === "user_prompt" ||
      (entry.presentation?.from !== undefined &&
        entry.presentation.from !== entry.agent_id &&
        entry.presentation.from !== "assistant") ||
      (entry.sender_identity !== undefined &&
        entry.sender_identity !== entry.agent_id &&
        entry.sender_identity !== "assistant"));

  const role = isUser
    ? (entry.sender_identity ?? entry.presentation?.from ?? "User")
    : "Assistant";
  return { role, text, isAssistant: !isUser };
}

export function SubagentViewerDialog({
  open,
  onClose,
  agentId: _agentId,
  subagents = [],
  client,
  onStopSubagent,
}: SubagentViewerDialogProps): ReactElement | null {
  const [selectedSubagentId, setSelectedSubagentId] = useState<string | null>(null);
  const [filter, setFilter] = useState<"active" | "all" | "completed">("active");
  const [historyEntries, setHistoryEntries] = useState<ManagedHistoryEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [stoppingIds, setStoppingIds] = useState<Set<string>>(() => new Set());
  const [nowMs, setNowMs] = useState<number>(() => Date.now());
  const [previews, setPreviews] = useState<Record<string, string>>({});
  const conversationContainerRef = useRef<HTMLDivElement>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  // Track sticky proximity state within 180px threshold, and detect subagent selection changes
  const isNearBottomRef = useRef(true);
  const prevSubagentIdRef = useRef<string | null>(null);
  const titleId = useId();

  // Periodic interval to update running duration for active subagents
  useEffect(() => {
    if (!open) return;
    const interval = setInterval(() => {
      setNowMs(Date.now());
    }, 1000);
    return () => clearInterval(interval);
  }, [open]);

  // Reset drill-down view if dialog closes
  useEffect(() => {
    if (!open) {
      setSelectedSubagentId(null);
      setHistoryEntries([]);
      setHistoryError(null);
      setLoading(false);
      isNearBottomRef.current = true;
      prevSubagentIdRef.current = null;
    }
  }, [open]);

  const sortedHistoryEntries = useMemo(
    () => [...historyEntries].sort(compareManagedHistoryEntries),
    [historyEntries],
  );

  const latestEntry = sortedHistoryEntries.at(-1);
  const latestEntryKey = latestEntry
    ? `${latestEntry.id}:${latestEntry.message?.length ?? 0}:${latestEntry.message_content?.length ?? 0}`
    : "";

  // Sticky auto-scroll model: auto-scroll to bottom on subagent switch or when near bottom, but lock position when reading older backlog
  useLayoutEffect(() => {
    if (!selectedSubagentId) {
      prevSubagentIdRef.current = null;
      isNearBottomRef.current = true;
      return;
    }

    const isSubagentSwitch = prevSubagentIdRef.current !== selectedSubagentId;
    prevSubagentIdRef.current = selectedSubagentId;

    const element = conversationContainerRef.current;
    if (!element) return;

    if (isSubagentSwitch) {
      isNearBottomRef.current = true;
      element.scrollTop = element.scrollHeight;
      const frame = requestAnimationFrame(() => {
        if (conversationContainerRef.current) {
          conversationContainerRef.current.scrollTop = conversationContainerRef.current.scrollHeight;
        }
      });
      return () => cancelAnimationFrame(frame);
    }

    if (!isNearBottomRef.current) {
      return;
    }

    element.scrollTop = element.scrollHeight;
    const frame = requestAnimationFrame(() => {
      if (!isNearBottomRef.current) return;
      if (conversationContainerRef.current) {
        conversationContainerRef.current.scrollTop = conversationContainerRef.current.scrollHeight;
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [selectedSubagentId, sortedHistoryEntries.length, latestEntryKey]);

  // Update sticky proximity state based on whether the user is within DEFAULT_AUTO_SCROLL_THRESHOLD (180px) of the bottom
  const onScroll = (): void => {
    const element = conversationContainerRef.current;
    if (!element) return;
    const { scrollTop, scrollHeight, clientHeight } = element;
    isNearBottomRef.current = shouldFollowToBottom(
      { scrollTop, scrollHeight, clientHeight },
      DEFAULT_AUTO_SCROLL_THRESHOLD
    );
  };

  const selectedSubagent = selectedSubagentId
    ? subagents.find((s) => s.id === selectedSubagentId) ?? null
    : null;

  // Poll active subagents in list view for last activity preview
  useEffect(() => {
    if (!open || selectedSubagentId !== null || !client) return;

    let active = true;

    const pollPreviews = async () => {
      const activeSubagents = subagents.filter(
        (s) => s.status === "running" || s.status === "queued",
      );

      await Promise.all(
        activeSubagents.map(async (subagent) => {
          try {
            const res = await client.managedHistory(subagent.id, "complete");
            if (!active) return;
            const entries = res.entries ?? [];
            if (entries.length === 0) return;
            const sorted = [...entries].sort(compareManagedHistoryEntries);
            // Look for the last meaningful preview or the last entry
            for (let i = sorted.length - 1; i >= 0; i--) {
              const preview = formatLastActivityPreview(sorted[i]);
              if (preview) {
                setPreviews((prev) => {
                  if (prev[subagent.id] === preview) return prev;
                  return { ...prev, [subagent.id]: preview };
                });
                return;
              }
            }
          } catch {
            // Ignore preview polling failures
          }
        }),
      );
    };

    void pollPreviews();

    const intervalId = window.setInterval(() => {
      void pollPreviews();
    }, 2000);

    return () => {
      active = false;
      window.clearInterval(intervalId);
    };
  }, [client, open, selectedSubagentId, subagents]);

  // Fetch and poll history when a subagent is selected
  useEffect(() => {
    if (!open || !selectedSubagentId || !client) {
      setHistoryEntries([]);
      setHistoryError(null);
      setLoading(false);
      return;
    }

    let active = true;

    const loadHistory = () => {
      setLoading(true);
      setHistoryError(null);
      client
        .managedHistory(selectedSubagentId, "complete")
        .then((res: ManagedHistoryResponse) => {
          if (!active) return;
          const sorted = [...(res.entries ?? [])].sort(compareManagedHistoryEntries);
          setHistoryEntries(sorted);
          setLoading(false);
        })
        .catch((err: unknown) => {
          if (!active) return;
          const msg = err instanceof Error ? err.message : "Failed to load subagent history";
          setHistoryError(msg);
          setLoading(false);
        });
    };

    loadHistory();

    const intervalId = window.setInterval(loadHistory, 2000);

    return () => {
      active = false;
      window.clearInterval(intervalId);
    };
  }, [client, open, selectedSubagentId]);

  const handleStop = async (e: MouseEvent<HTMLButtonElement>, subagentId: string) => {
    e.stopPropagation();
    if (!onStopSubagent || stoppingIds.has(subagentId)) return;
    setStoppingIds((prev) => new Set(prev).add(subagentId));
    try {
      await onStopSubagent(subagentId);
    } catch {
      setStoppingIds((prev) => {
        const next = new Set(prev);
        next.delete(subagentId);
        return next;
      });
    }
  };

  // Filter counts and filtered list
  const activeCount = subagents.filter(
    (s) => s.status === "running" || s.status === "queued",
  ).length;
  const completedCount = subagents.filter(
    (s) => s.status === "completed" || s.status === "stopped",
  ).length;
  const totalCount = subagents.length;

  const filteredSubagents = subagents
    .filter((s) => {
      if (filter === "active") {
        return s.status === "running" || s.status === "queued";
      }
      if (filter === "completed") {
        return s.status === "completed" || s.status === "stopped";
      }
      return true;
    })
    .sort((a, b) => {
      const timeDiff = getSubagentTime(b) - getSubagentTime(a);
      if (timeDiff !== 0) return timeDiff;
      const seqA = (a as Partial<ManagedChild>).sequence ?? 0;
      const seqB = (b as Partial<ManagedChild>).sequence ?? 0;
      return seqB - seqA;
    });

  const dialogTitle = selectedSubagent
    ? selectedSubagent.description || "Subagent Details"
    : `Subagents (${subagents.length})`;

  const selectedChild = selectedSubagent as (SubagentSummary & Partial<ManagedChild>) | null;
  const selectedIsActive = selectedSubagent?.status === "running" || selectedSubagent?.status === "queued";
  const selectedDuration = formatDuration(
    selectedChild?.started_at,
    selectedChild?.ended_at,
    selectedChild?.duration_ms,
    nowMs,
  );

  const renderedHistoryItems = useMemo(() => {
    let lastThinking: string | undefined;
    const result: Array<{ entry: ManagedHistoryEntry; suppressThinking: boolean }> = [];
    for (const entry of sortedHistoryEntries) {
      const isThinkingKind = entry.presentation?.kind === "thinking";
      const thinkingText = getThinkingContent(entry);
      if (thinkingText) {
        if (thinkingText === lastThinking) {
          if (isThinkingKind) {
            continue;
          }
          result.push({ entry, suppressThinking: true });
          continue;
        }
        lastThinking = thinkingText;
      }
      result.push({ entry, suppressThinking: false });
    }
    return result;
  }, [sortedHistoryEntries]);

  return (
    <DialogShell
      open={open}
      title={dialogTitle}
      onClose={onClose}
      className="subagent-viewer-dialog message-dialog"
      titleId={titleId}
    >
      <div className="subagent-viewer-body">
        {selectedSubagentId !== null ? (
          <div className="subagent-drilldown-view">
            <header className="subagent-drilldown-header">
              <button
                type="button"
                className="icon-btn-round subagent-back-button"
                onClick={() => setSelectedSubagentId(null)}
                aria-label="Back to subagents"
                title="Back to subagents"
              >
                <i className="fa-solid fa-arrow-left" aria-hidden="true" />
              </button>
              <div className="subagent-drilldown-info">
                {selectedSubagent ? (
                  <span
                    className={`subagent-status-pill status-${selectedSubagent.status || "unknown"}`}
                  >
                    {selectedSubagent.status === "running" && (
                      <span className="subagent-dot" aria-hidden="true" />
                    )}
                    {selectedSubagent.status || "unknown"}
                  </span>
                ) : null}
                {selectedDuration ? (
                  <span className="subagent-duration-badge" title={selectedIsActive ? "Running duration" : "Total duration"}>
                    {selectedIsActive ? `Running for ${selectedDuration}` : `Duration: ${selectedDuration}`}
                  </span>
                ) : null}
              </div>
            </header>

            <div
              ref={conversationContainerRef}
              className="subagent-conversation-view"
              style={{ overflowY: "auto" }}
              onScroll={onScroll}
            >
              {loading && sortedHistoryEntries.length === 0 ? (
                <div className="subagent-loading-state">
                  <span className="reconnecting-pulse-dot" aria-hidden="true" />
                  <p>Loading subagent history...</p>
                </div>
              ) : historyError ? (
                <div className="subagent-error-state">
                  <p className="error-text">{historyError}</p>
                </div>
              ) : sortedHistoryEntries.length === 0 ? (
                <div className="subagent-empty-history">
                  <p>No activity recorded for this subagent yet.</p>
                </div>
              ) : (
                <div className="subagent-timeline-entries">
                  {renderedHistoryItems.map(({ entry, suppressThinking }) => {
                    const msg = getMessageEntryDetails(entry);
                    if (msg) {
                      const thinking = suppressThinking ? undefined : getThinkingContent(entry);
                      return (
                        <div key={entry.id} className="subagent-message-block">
                          {thinking ? (
                            <ThinkingTraceCard thinking={thinking} agentId={entry.agent_id} entry={entry} />
                          ) : null}
                          <div className={`subagent-message-entry ${msg.isAssistant ? "assistant" : "user"}`}>
                            <div className="subagent-message-role">{msg.role}</div>
                            <MarkdownBody body={msg.text} />
                          </div>
                        </div>
                      );
                    }
                    return (
                      <CompleteTimelineEntry
                        key={entry.id}
                        entry={entry}
                        suppressThinking={suppressThinking}
                      />
                    );
                  })}
                  <div ref={messagesEndRef} aria-hidden="true" />
                </div>
              )}
            </div>
          </div>
        ) : (
          <div className="subagent-list-view">
            <div className="subagent-list-toolbar">
              <label htmlFor="subagent-filter-select" className="subagent-filter-label">Filter:</label>
              <select
                id="subagent-filter-select"
                className="subagent-filter-select"
                value={filter}
                onChange={(e) => setFilter(e.target.value as "active" | "all" | "completed")}
              >
                <option value="active">Active ({activeCount})</option>
                <option value="all">All ({totalCount})</option>
                <option value="completed">Completed ({completedCount})</option>
              </select>
            </div>
            {filteredSubagents.length === 0 ? (
              <div className="subagent-empty-state">
                <p>No subagents match the selected filter.</p>
              </div>
            ) : (
              <div className="subagent-card-grid">
                {filteredSubagents.map((subagent) => {
                  const child = subagent as SubagentSummary & Partial<ManagedChild>;
                  const isActive =
                    subagent.status === "running" || subagent.status === "queued";
                  const isStopping = stoppingIds.has(subagent.id);
                  const duration = formatDuration(
                    child.started_at,
                    child.ended_at,
                    child.duration_ms,
                    nowMs,
                  );
                  const started = formatTimestamp(child.started_at);

                  return (
                    <div
                      key={subagent.id}
                      className={`subagent-card ${isActive ? "active" : ""}`}
                      onClick={() => setSelectedSubagentId(subagent.id)}
                      role="button"
                      tabIndex={0}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          setSelectedSubagentId(subagent.id);
                        }
                      }}
                    >
                      <div className="subagent-card-header">
                        <h3 className="subagent-card-title" title={subagent.description || "Subagent"}>
                          <i className="fa-solid fa-robot subagent-card-robot-icon" aria-hidden="true" />
                          <span className="subagent-card-title-text">{subagent.description || "Subagent"}</span>
                        </h3>
                        <span className={`subagent-status-pill status-${subagent.status || "unknown"}`}>
                          {isActive && <span className="subagent-dot" aria-hidden="true" />}
                          {subagent.status || "unknown"}
                        </span>
                      </div>

                      <div className="subagent-card-meta">
                        {child.sequence != null && (
                          <span className="subagent-meta-item">Turn {child.sequence}</span>
                        )}
                        {started && (
                          <span className="subagent-meta-item" title={`Started at ${child.started_at}`}>
                            Started {started}
                          </span>
                        )}
                        {duration && (
                          <span className="subagent-meta-item" title={isActive ? "Running duration" : "Duration"}>
                            {isActive ? `Running for ${duration}` : `Duration: ${duration}`}
                          </span>
                        )}
                      </div>

                      <div className="subagent-card-preview">
                        <p title={previews[subagent.id] || "No activity recorded yet"}>
                          {previews[subagent.id] || (isActive ? "Running..." : "No activity recorded")}
                        </p>
                      </div>

                      <div className="subagent-card-footer">
                        {isActive && onStopSubagent && (
                          <button
                            type="button"
                            className="subagent-card-stop-btn"
                            disabled={isStopping}
                            onClick={(e) => void handleStop(e, subagent.id)}
                            title="Stop subagent"
                          >
                            {isStopping ? "Stopping..." : "Stop"}
                          </button>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </div>
    </DialogShell>
  );
}
