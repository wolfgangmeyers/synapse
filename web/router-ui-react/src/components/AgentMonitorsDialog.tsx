import { useState, useEffect, type ReactElement } from "react";
import type { RouterHttpClient, AgentMonitor, CreateAgentMonitorPayload, MonitorEvaluationResult } from "../transport/http-client";
import { DialogShell } from "./DialogShell";
import { MONITOR_PRESETS, getMonitorPresetById } from "../domain/monitor-presets";

export interface AgentMonitorsDialogProps {
  agentId: string;
  isOpen: boolean;
  onClose: () => void;
  client?: RouterHttpClient | undefined;
}

const DEFAULT_NEW_MONITOR: CreateAgentMonitorPayload = {
  name: "",
  description: "",
  enabled: true,
  interval_seconds: 30,
  cooldown_seconds: 120,
  max_alerts: 0,
  trailing_messages_count: 6,
  question_type: "noul",
  question_instructions: "",
  condition_operator: "gte",
  condition_threshold: "0.8",
  nudge_template: "",
  fallback_mode: "skip",
};

export function AgentMonitorsDialog({
  agentId,
  isOpen,
  onClose,
  client,
}: AgentMonitorsDialogProps): ReactElement | null {
  const [monitors, setMonitors] = useState<AgentMonitor[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // New Monitor Form state
  const [selectedPresetId, setSelectedPresetId] = useState<string>("");
  const [formData, setFormData] = useState<CreateAgentMonitorPayload>({ ...DEFAULT_NEW_MONITOR });
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [saving, setSaving] = useState(false);

  // Testing evaluation state
  const [evaluating, setEvaluating] = useState<string | null>(null);
  const [evalResult, setEvalResult] = useState<MonitorEvaluationResult | null>(null);

  const fetchMonitors = async () => {
    if (!client || !agentId) return;
    setLoading(true);
    setError(null);
    try {
      const data = await client.listAgentMonitors(agentId);
      setMonitors(data);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to load agent monitors");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (isOpen) {
      void fetchMonitors();
      setEvalResult(null);
      setError(null);
    }
  }, [isOpen, agentId]);

  const handlePresetChange = (presetId: string) => {
    setSelectedPresetId(presetId);
    if (!presetId) return;
    const preset = getMonitorPresetById(presetId);
    if (preset) {
      setFormData({
        ...DEFAULT_NEW_MONITOR,
        ...preset.payload,
      });
    }
  };

  const handleToggleEnabled = async (mon: AgentMonitor) => {
    if (!client) return;
    try {
      const updated = await client.updateAgentMonitor(agentId, mon.id, {
        enabled: !mon.enabled,
      });
      setMonitors((prev) => prev.map((m) => (m.id === mon.id ? updated : m)));
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to update monitor state");
    }
  };

  const handleDelete = async (monId: string) => {
    if (!client) return;
    try {
      await client.deleteAgentMonitor(agentId, monId);
      setMonitors((prev) => prev.filter((m) => m.id !== monId));
      if (evalResult?.monitor_id === monId) {
        setEvalResult(null);
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete monitor");
    }
  };

  const handleTestEvaluate = async (monId: string) => {
    if (!client) return;
    setEvaluating(monId);
    setEvalResult(null);
    setError(null);
    try {
      const result = await client.evaluateAgentMonitor(agentId, monId);
      setEvalResult(result);
      void fetchMonitors();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Evaluation failed");
    } finally {
      setEvaluating(null);
    }
  };

  const handleCreateMonitor = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!client) return;
    if (!formData.name.trim()) {
      setError("Monitor Name is required");
      return;
    }
    if (!formData.nudge_template.trim()) {
      setError("Nudge Message is required");
      return;
    }
    if (!formData.question_instructions.trim()) {
      setError("Question Instructions are required");
      return;
    }

    setSaving(true);
    setError(null);
    try {
      const created = await client.createAgentMonitor(agentId, formData);
      setMonitors((prev) => [...prev, created]);
      // Reset form
      setFormData({ ...DEFAULT_NEW_MONITOR });
      setSelectedPresetId("");
      setShowAdvanced(false);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to create monitor");
    } finally {
      setSaving(false);
    }
  };

  if (!isOpen) return null;

  return (
    <DialogShell
      open={isOpen}
      title="System 1 Background Monitors"
      titleId="agent-monitors-dialog-title"
      description="Configure real-time automated System 1 background evaluations and nudges for this agent."
      onClose={onClose}
      closeLabel="Close agent monitors dialog"
      className="message-dialog agent-monitors-dialog"
    >
      <div className="agent-monitors-dialog-body" style={{ display: "flex", flexDirection: "column", gap: "1.25rem", maxHeight: "80vh", overflowY: "auto", paddingRight: "4px" }}>
        {error ? (
          <div
            className="alert alert-danger"
            role="alert"
            style={{
              color: "#f87171",
              backgroundColor: "rgba(127, 29, 29, 0.4)",
              border: "1px solid #991b1b",
              borderRadius: "6px",
              padding: "8px 12px",
              fontSize: "13px",
            }}
          >
            {error}
          </div>
        ) : null}

        {/* Existing monitors list */}
        <div className="monitors-list-section">
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "0.5rem" }}>
            <h3 style={{ fontSize: "1rem", margin: 0, fontWeight: 600 }}>Active Monitors ({monitors.length})</h3>
            {loading ? <span style={{ fontSize: "0.8rem", color: "var(--text-muted, #888)" }}>Loading...</span> : null}
          </div>

          {monitors.length === 0 && !loading ? (
            <div style={{ padding: "12px", background: "rgba(255,255,255,0.03)", borderRadius: "6px", fontSize: "0.85rem", color: "var(--text-muted, #888)" }}>
              No monitors configured for this agent yet. Choose a preset below to get started.
            </div>
          ) : (
            <div style={{ display: "flex", flexDirection: "column", gap: "0.75rem" }}>
              {monitors.map((mon) => (
                <div
                  key={mon.id}
                  style={{
                    background: "rgba(255,255,255,0.04)",
                    border: "1px solid var(--border-color, #333)",
                    borderRadius: "6px",
                    padding: "10px 12px",
                    display: "flex",
                    flexDirection: "column",
                    gap: "8px",
                  }}
                >
                  <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                    <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                      <label className="switch-toggle" style={{ transform: "scale(0.85)" }}>
                        <input
                          type="checkbox"
                          checked={mon.enabled}
                          onChange={() => void handleToggleEnabled(mon)}
                          aria-label={`Toggle ${mon.name}`}
                        />
                        <span className="switch-slider" />
                      </label>
                      <span style={{ fontWeight: 600, fontSize: "0.95rem" }}>{mon.name}</span>
                    </div>

                    <div style={{ display: "flex", alignItems: "center", gap: "6px" }}>
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        style={{ fontSize: "0.75rem", padding: "2px 8px" }}
                        onClick={() => void handleTestEvaluate(mon.id)}
                        disabled={evaluating === mon.id}
                        title="Evaluate this monitor right now against recent history"
                      >
                        {evaluating === mon.id ? "Evaluating..." : "Test Run"}
                      </button>
                      <button
                        type="button"
                        className="btn-icon btn-danger"
                        style={{ padding: "4px 8px", background: "none", border: "none", cursor: "pointer", color: "#f87171" }}
                        onClick={() => void handleDelete(mon.id)}
                        title="Delete monitor"
                        aria-label={`Delete ${mon.name}`}
                      >
                        <i className="fa-solid fa-trash" aria-hidden="true" />
                      </button>
                    </div>
                  </div>

                  <div style={{ display: "flex", flexWrap: "wrap", gap: "6px", fontSize: "0.75rem" }}>
                    <span style={{ background: "rgba(255,255,255,0.08)", padding: "2px 6px", borderRadius: "4px" }}>
                      Every {mon.interval_seconds}s
                    </span>
                    <span style={{ background: "rgba(255,255,255,0.08)", padding: "2px 6px", borderRadius: "4px" }}>
                      Cooldown: {mon.cooldown_seconds}s
                    </span>
                    <span style={{ background: "rgba(255,255,255,0.08)", padding: "2px 6px", borderRadius: "4px" }}>
                      Alerts: {mon.consecutive_alerts}{mon.max_alerts > 0 ? `/${mon.max_alerts}` : " (unlimited)"}
                    </span>
                    <span style={{ color: "var(--text-muted, #888)", alignSelf: "center", marginLeft: "auto" }}>
                      {mon.last_evaluated_at ? `Evaluated: ${new Date(mon.last_evaluated_at).toLocaleTimeString()}` : "Never evaluated"}
                    </span>
                  </div>

                  {mon.nudge_template ? (
                    <div style={{ fontSize: "0.8rem", color: "var(--text-secondary, #bbb)", background: "rgba(0,0,0,0.2)", padding: "6px 8px", borderRadius: "4px" }}>
                      <span style={{ color: "var(--text-muted, #888)", fontWeight: 500 }}>Nudge: </span>
                      {mon.nudge_template}
                    </div>
                  ) : null}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Evaluation result box */}
        {evalResult ? (
          <div
            className="evaluation-result-box"
            style={{
              padding: "10px 12px",
              background: "rgba(0,0,0,0.3)",
              border: `1px solid ${evalResult.matched ? "var(--color-danger, #ef4444)" : "var(--color-success, #22c55e)"}`,
              borderRadius: "6px",
              fontSize: "0.85rem",
            }}
          >
            <div style={{ fontWeight: 600, marginBottom: "4px", display: "flex", justifyContent: "space-between" }}>
              <span>
                Evaluation Result: {evalResult.skipped ? "SKIPPED" : evalResult.matched ? "TRIGGERED (Matched)" : "PASSED (No Match)"}
              </span>
              <span style={{ fontSize: "0.75rem", color: "var(--text-muted, #888)" }}>
                {new Date(evalResult.evaluated_at || Date.now()).toLocaleTimeString()}
              </span>
            </div>
            {evalResult.skip_reason ? (
              <div style={{ color: "var(--text-muted, #888)" }}>Reason: {evalResult.skip_reason}</div>
            ) : null}
            <div style={{ display: "flex", gap: "12px", fontSize: "0.8rem", marginTop: "4px" }}>
              <span>Matched: <strong>{String(evalResult.matched)}</strong></span>
              <span>Skipped: <strong>{String(evalResult.skipped)}</strong></span>
              {evalResult.actual_value !== undefined ? (
                <span>Value: <strong>{JSON.stringify(evalResult.actual_value)}</strong></span>
              ) : null}
              {evalResult.alert_fired !== undefined ? (
                <span>Alert Fired: <strong>{String(evalResult.alert_fired)}</strong></span>
              ) : null}
            </div>
            {evalResult.error ? (
              <div style={{ color: "#f87171", marginTop: "4px" }}>Error: {evalResult.error}</div>
            ) : null}
          </div>
        ) : null}

        {/* New Monitor Form */}
        <form onSubmit={(e) => void handleCreateMonitor(e)} className="new-monitor-form" style={{ borderTop: "1px solid var(--border-color, #333)", paddingTop: "1rem" }}>
          <h3 style={{ fontSize: "1rem", marginBottom: "0.75rem", fontWeight: 600 }}>Create New Monitor</h3>

          {/* Preset picker */}
          <div className="form-group" style={{ marginBottom: "0.75rem" }}>
            <label htmlFor="monitor-preset-select" style={{ display: "block", marginBottom: "0.3rem", fontSize: "0.85rem", fontWeight: 600 }}>
              Load From Preset
            </label>
            <select
              id="monitor-preset-select"
              className="form-control"
              value={selectedPresetId}
              onChange={(e) => handlePresetChange(e.target.value)}
            >
              <option value="">-- Choose a Preset --</option>
              {MONITOR_PRESETS.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </div>

          {/* Monitor Name */}
          <div className="form-group" style={{ marginBottom: "0.75rem" }}>
            <label htmlFor="monitor-name-input" style={{ display: "block", marginBottom: "0.3rem", fontSize: "0.85rem", fontWeight: 600 }}>
              Name *
            </label>
            <input
              id="monitor-name-input"
              type="text"
              className="form-control"
              value={formData.name}
              onChange={(e) => setFormData({ ...formData, name: e.target.value })}
              placeholder="e.g. Loop & Stall Detector"
              required
            />
          </div>

          {/* Nudge Message */}
          <div className="form-group" style={{ marginBottom: "0.75rem" }}>
            <label htmlFor="monitor-nudge-input" style={{ display: "block", marginBottom: "0.3rem", fontSize: "0.85rem", fontWeight: 600 }}>
              Nudge Message * (injected into agent prompt when triggered)
            </label>
            <textarea
              id="monitor-nudge-input"
              className="form-control"
              rows={2}
              value={formData.nudge_template}
              onChange={(e) => setFormData({ ...formData, nudge_template: e.target.value })}
              placeholder="Instruction or steering message delivered to the agent..."
              required
            />
          </div>

          {/* Intervals & Cooldowns */}
          <div style={{ display: "flex", gap: "10px", marginBottom: "0.75rem" }}>
            <div style={{ flex: 1 }}>
              <label htmlFor="monitor-interval-select" style={{ display: "block", marginBottom: "0.3rem", fontSize: "0.85rem", fontWeight: 600 }}>
                Evaluation Interval
              </label>
              <select
                id="monitor-interval-select"
                className="form-control"
                value={formData.interval_seconds}
                onChange={(e) => setFormData({ ...formData, interval_seconds: Number(e.target.value) })}
              >
                <option value={15}>Every 15 seconds</option>
                <option value={30}>Every 30 seconds</option>
                <option value={60}>Every 60 seconds</option>
                <option value={120}>Every 2 minutes</option>
                <option value={300}>Every 5 minutes</option>
              </select>
            </div>

            <div style={{ flex: 1 }}>
              <label htmlFor="monitor-cooldown-select" style={{ display: "block", marginBottom: "0.3rem", fontSize: "0.85rem", fontWeight: 600 }}>
                Cooldown Period
              </label>
              <select
                id="monitor-cooldown-select"
                className="form-control"
                value={formData.cooldown_seconds}
                onChange={(e) => setFormData({ ...formData, cooldown_seconds: Number(e.target.value) })}
              >
                <option value={60}>1 minute</option>
                <option value={120}>2 minutes</option>
                <option value={300}>5 minutes</option>
                <option value={600}>10 minutes</option>
              </select>
            </div>
          </div>

          {/* Question instructions */}
          <div className="form-group" style={{ marginBottom: "0.75rem" }}>
            <label htmlFor="monitor-instructions-input" style={{ display: "block", marginBottom: "0.3rem", fontSize: "0.85rem", fontWeight: 600 }}>
              System 1 Question Instructions *
            </label>
            <textarea
              id="monitor-instructions-input"
              className="form-control"
              rows={2}
              value={formData.question_instructions}
              onChange={(e) => setFormData({ ...formData, question_instructions: e.target.value })}
              placeholder="Question evaluated by System 1 model (Clef) against recent transcript..."
              required
            />
          </div>

          {/* Advanced Settings Accordion */}
          <div style={{ marginBottom: "1rem" }}>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              style={{ display: "flex", alignItems: "center", gap: "6px", fontSize: "0.8rem", background: "none", border: "none", color: "var(--text-muted, #888)", cursor: "pointer", padding: "4px 0" }}
              onClick={() => setShowAdvanced((v) => !v)}
            >
              <i className={`fa-solid ${showAdvanced ? "fa-chevron-down" : "fa-chevron-right"}`} aria-hidden="true" />
              <span>Advanced Settings</span>
            </button>

            {showAdvanced ? (
              <div
                className="advanced-settings-panel"
                style={{
                  marginTop: "8px",
                  padding: "10px",
                  background: "rgba(0,0,0,0.15)",
                  border: "1px solid var(--border-color, #333)",
                  borderRadius: "6px",
                  display: "flex",
                  flexDirection: "column",
                  gap: "10px",
                }}
              >
                <div style={{ display: "flex", gap: "10px" }}>
                  <div style={{ flex: 1 }}>
                    <label htmlFor="monitor-qtype-select" style={{ display: "block", marginBottom: "0.2rem", fontSize: "0.8rem" }}>
                      Question Type
                    </label>
                    <select
                      id="monitor-qtype-select"
                      className="form-control form-control-sm"
                      value={formData.question_type}
                      onChange={(e) => setFormData({ ...formData, question_type: e.target.value as "noul" | "choice" | "score" })}
                    >
                      <option value="noul">Noul (0.0 to 1.0)</option>
                      <option value="choice">Choice</option>
                      <option value="score">Score</option>
                    </select>
                  </div>

                  <div style={{ flex: 1 }}>
                    <label htmlFor="monitor-op-select" style={{ display: "block", marginBottom: "0.2rem", fontSize: "0.8rem" }}>
                      Condition Operator
                    </label>
                    <select
                      id="monitor-op-select"
                      className="form-control form-control-sm"
                      value={formData.condition_operator}
                      onChange={(e) => setFormData({ ...formData, condition_operator: e.target.value })}
                    >
                      <option value="gte">&gt;= (Greater or equal)</option>
                      <option value="gt">&gt; (Greater than)</option>
                      <option value="lte">&lt;= (Less or equal)</option>
                      <option value="lt">&lt; (Less than)</option>
                      <option value="eq">== (Equal)</option>
                      <option value="neq">!= (Not equal)</option>
                    </select>
                  </div>

                  <div style={{ flex: 1 }}>
                    <label htmlFor="monitor-threshold-input" style={{ display: "block", marginBottom: "0.2rem", fontSize: "0.8rem" }}>
                      Condition Threshold
                    </label>
                    <input
                      id="monitor-threshold-input"
                      type="text"
                      className="form-control form-control-sm"
                      value={formData.condition_threshold}
                      onChange={(e) => setFormData({ ...formData, condition_threshold: e.target.value })}
                    />
                  </div>
                </div>

                <div style={{ display: "flex", gap: "10px" }}>
                  <div style={{ flex: 1 }}>
                    <label htmlFor="monitor-trailing-input" style={{ display: "block", marginBottom: "0.2rem", fontSize: "0.8rem" }}>
                      Trailing Messages Count
                    </label>
                    <input
                      id="monitor-trailing-input"
                      type="number"
                      min={1}
                      max={50}
                      className="form-control form-control-sm"
                      value={formData.trailing_messages_count ?? 6}
                      onChange={(e) => setFormData({ ...formData, trailing_messages_count: Number(e.target.value) })}
                    />
                  </div>

                  <div style={{ flex: 1 }}>
                    <label htmlFor="monitor-max-alerts-input" style={{ display: "block", marginBottom: "0.2rem", fontSize: "0.8rem" }}>
                      Max Alerts (0 for unlimited)
                    </label>
                    <input
                      id="monitor-max-alerts-input"
                      type="number"
                      min={0}
                      className="form-control form-control-sm"
                      value={formData.max_alerts ?? 0}
                      onChange={(e) => setFormData({ ...formData, max_alerts: Number(e.target.value) })}
                    />
                  </div>
                </div>
              </div>
            ) : null}
          </div>

          <div style={{ display: "flex", justifyContent: "flex-end", gap: "8px" }}>
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? "Saving..." : "Save Monitor"}
            </button>
          </div>
        </form>
      </div>
    </DialogShell>
  );
}
