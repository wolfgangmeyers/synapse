import { useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName, isAgentActive, isCompletedAgent } from "../state/selectors";
import { secureUuid } from "../platform/secure-uuid";
import { DialogShell } from "./DialogShell";
import { AgentAvatar } from "./AgentAvatar";

export interface BroadcastMessageDialogProps {
  open: boolean;
  client: { sendMessage: (message: unknown, signal?: AbortSignal) => Promise<Record<string, unknown>> };
  currentAgentId: string;
  agents: Agent[];
  onClose: () => void;
}

interface RecipientResult {
  status: "queued" | "delivered" | "error";
  error?: string;
}

function agentStatus(agent: Agent): string {
  if (isCompletedAgent(agent)) {
    const lifecycle = agent.lifecycle_state ?? agent.managed_agent?.lifecycle_state;
    if (lifecycle === "failed" || agent.activity_state === "failed" || agent.managed_agent?.activity_state === "failed") {
      return "failed";
    }
    return "stopped";
  }
  const activity = agent.activity_state ?? agent.managed_agent?.activity_state;
  if (activity === "active") return "active";
  if (activity === "idle") return "idle";
  if (activity === "compacting") return "compacting";
  if (activity === "starting" || agent.lifecycle_state === "starting" || agent.managed_agent?.lifecycle_state === "starting") return "starting";
  return activity ?? "active";
}

/**
 * BroadcastMessageDialog
 *
 * Modal dialog for composing and broadcasting a message to multiple agents.
 * Excludes the active user session from targets and defaults selection to
 * currently running workers. Dispatches distinct message envelopes sequentially
 * via POST /v1/messages so failures stay isolated per recipient.
 */
export function BroadcastMessageDialog({
  open,
  client,
  currentAgentId,
  agents,
  onClose,
}: BroadcastMessageDialogProps): ReactElement {
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [sending, setSending] = useState(false);
  const [sendingProgress, setSendingProgress] = useState<string | null>(null);
  const [dispatchResults, setDispatchResults] = useState<Record<string, RecipientResult>>({});
  const [dispatchSummary, setDispatchSummary] = useState<{ type: "success" | "error"; text: string } | null>(null);

  // Selection logic: exclude current user session, sort active workers first
  // (alphabetical secondary sort), and default selection to running agents when opened.
  const availableAgents = useMemo(() => {
    return agents
      .filter((agent) => agent.agent_id !== currentAgentId)
      .sort((a, b) => {
        const aActive = isAgentActive(a) ? 1 : 0;
        const bActive = isAgentActive(b) ? 1 : 0;
        if (aActive !== bActive) return bActive - aActive;
        return a.agent_id.localeCompare(b.agent_id);
      });
  }, [agents, currentAgentId]);

  const activeCount = useMemo(() => {
    return availableAgents.filter(isAgentActive).length;
  }, [availableAgents]);

  const prevOpenRef = useRef(false);
  // On modal open, reset inputs and default recipient selection to active workers.
  useEffect(() => {
    if (open && !prevOpenRef.current) {
      const active = availableAgents.filter(isAgentActive).map((a) => a.agent_id);
      setSelected(new Set(active));
      setSubject("");
      setBody("");
      setSending(false);
      setSendingProgress(null);
      setDispatchResults({});
      setDispatchSummary(null);
    }
    prevOpenRef.current = open;
  }, [open, availableAgents]);

  const toggleRecipient = (agentId: string) => {
    if (sending) return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(agentId)) {
        next.delete(agentId);
      } else {
        next.add(agentId);
      }
      return next;
    });
  };

  // Sequential dispatch loop:
  // 1. Sends individual message envelopes via POST /v1/messages with unique IDs.
  // 2. Isolates recipient failures in try/catch so remaining dispatches continue.
  // 3. Tracks live progress ("Sending X of Y...") and per-recipient status indicators.
  // 4. Tallies success/error metrics and renders a summary notice on completion.
  const handleSend = async () => {
    if (sending || selected.size === 0 || !body.trim()) return;
    setSending(true);
    setDispatchSummary(null);
    const recipientIds = Array.from(selected);
    const total = recipientIds.length;
    let successCount = 0;
    let errorCount = 0;
    const results: Record<string, RecipientResult> = {};

    for (const id of recipientIds) {
      results[id] = { status: "queued" };
    }
    setDispatchResults({ ...results });

    for (let i = 0; i < recipientIds.length; i++) {
      const recipientId = recipientIds[i];
      if (!recipientId) continue;
      setSendingProgress(`Sending ${i + 1} of ${total}...`);
      try {
        const payload: Record<string, unknown> = {
          id: secureUuid(),
          from: currentAgentId,
          to: recipientId,
          ...(subject.trim() ? { subject: subject.trim() } : {}),
          body: body.trim(),
          created_at: new Date().toISOString(),
          source: { kind: "web_ui" },
        };
        await client.sendMessage(payload);
        results[recipientId] = { status: "delivered" };
        successCount++;
      } catch (err: unknown) {
        const errMsg = err instanceof Error ? err.message : String(err);
        results[recipientId] = { status: "error", error: errMsg };
        errorCount++;
      }
      setDispatchResults({ ...results });
    }

    setSending(false);
    setSendingProgress(null);
    if (errorCount === 0) {
      setDispatchSummary({
        type: "success",
        text: `Sent to ${successCount} agents successfully.`,
      });
    } else {
      setDispatchSummary({
        type: "error",
        text: `Sent to ${successCount} agents; ${errorCount} failed.`,
      });
    }
  };

  const footer = (
    <div className="dialog-actions broadcast-dialog-actions">
      <button
        type="button"
        className="btn btn-secondary"
        onClick={onClose}
        disabled={sending}
      >
        Cancel
      </button>
      <button
        type="button"
        className="btn btn-primary"
        onClick={() => void handleSend()}
        disabled={sending || selected.size === 0 || !body.trim()}
      >
        {sending ? "Sending..." : "Send Broadcast"}
      </button>
    </div>
  );

  return (
    <DialogShell
      open={open}
      title="Broadcast Message"
      titleId="broadcast-dialog-title"
      description="Send a message to multiple agents at once."
      onClose={onClose}
      closeLabel="Close broadcast dialog"
      className="message-dialog broadcast-dialog"
      footer={footer}
    >
      <div className="broadcast-dialog-content">
        <div className="broadcast-toolbar">
          <span className="broadcast-count-label">
            Selected {selected.size} of {availableAgents.length} agents ({activeCount} active)
          </span>
          <div className="broadcast-toolbar-actions">
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={sending}
              onClick={() => setSelected(new Set(availableAgents.filter(isAgentActive).map((a) => a.agent_id)))}
            >
              Select Active
            </button>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={sending}
              onClick={() => setSelected(new Set(availableAgents.map((a) => a.agent_id)))}
            >
              Select All
            </button>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              disabled={sending}
              onClick={() => setSelected(new Set())}
            >
              Deselect All
            </button>
          </div>
        </div>

        <div className="broadcast-recipient-list" role="group" aria-label="Broadcast recipients">
          {availableAgents.length === 0 ? (
            <p className="broadcast-empty">No agents available.</p>
          ) : (
            availableAgents.map((agent) => {
              const status = agentStatus(agent);
              const result = dispatchResults[agent.agent_id];
              return (
                <label
                  key={agent.agent_id}
                  className={`broadcast-recipient-row ${selected.has(agent.agent_id) ? "selected" : ""}`}
                >
                  <input
                    type="checkbox"
                    checked={selected.has(agent.agent_id)}
                    disabled={sending}
                    onChange={() => toggleRecipient(agent.agent_id)}
                  />
                  <span className="broadcast-recipient-avatar">
                    <AgentAvatar agent={agent} />
                  </span>
                  <span className="broadcast-agent-name">{humanName(agent.agent_id)}</span>
                  <span className="broadcast-agent-id">({agent.agent_id})</span>
                  <span className={`broadcast-status-badge status-${status}`}>{status}</span>
                  {result ? (
                    <span
                      className={`broadcast-recipient-status ${result.status}`}
                      title={result.error ?? result.status}
                    >
                      {result.status === "delivered" && (
                        <i className="fa-solid fa-check" aria-hidden="true" />
                      )}
                      {result.status === "queued" && (
                        <i className="fa-solid fa-clock" aria-hidden="true" />
                      )}
                      {result.status === "error" && (
                        <>
                          <i className="fa-solid fa-triangle-exclamation" aria-hidden="true" />
                          {result.error ? (
                            <span className="broadcast-error-msg">{result.error}</span>
                          ) : null}
                        </>
                      )}
                    </span>
                  ) : null}
                </label>
              );
            })
          )}
        </div>

        <div className="broadcast-composer">
          <input
            type="text"
            className="broadcast-subject-input"
            placeholder="Subject (optional)"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            maxLength={256}
            disabled={sending}
          />
          <textarea
            className="broadcast-body-input"
            placeholder="Write broadcast message..."
            value={body}
            onChange={(e) => setBody(e.target.value)}
            rows={4}
            required
            disabled={sending}
          />
        </div>

        {sendingProgress ? (
          <div className="broadcast-progress" role="status">
            <span className="broadcast-spinner" aria-hidden="true" />
            <span>{sendingProgress}</span>
          </div>
        ) : null}

        {dispatchSummary ? (
          <div className={`broadcast-summary ${dispatchSummary.type}`} role="alert">
            {dispatchSummary.text}
          </div>
        ) : null}
      </div>
    </DialogShell>
  );
}
