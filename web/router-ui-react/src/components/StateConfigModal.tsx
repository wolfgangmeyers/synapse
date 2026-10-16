import { useState, useEffect, type ReactElement, type FormEvent } from "react";
import type { WorkflowState, WorkflowReminderConfig, WorkflowMonitorConfig, KnownToolDefinition } from "../domain/workflow-states";
import { KNOWN_WORKFLOW_TOOLS } from "../domain/workflow-states";
import { DEFAULT_GOAL_STATE, type GoalState, type goalControlPayload } from "../domain/goals";
import { MONITOR_PRESETS, getMonitorPresetById } from "../domain/monitor-presets";
import { secureUuid } from "../platform/secure-uuid";
import { DialogShell } from "./DialogShell";
import { GoalEditorDialog } from "./GoalEditorDialog";

export interface StateConfigModalProps {
  state: WorkflowState;
  isEntry: boolean;
  tools?: KnownToolDefinition[] | undefined;
  onClose: () => void;
  onSave: (updatedState: WorkflowState, makeEntry: boolean) => Promise<void>;
}

export function StateConfigModal({ state, isEntry, tools: propTools, onClose, onSave }: StateConfigModalProps): ReactElement {
  const [availableTools, setAvailableTools] = useState<KnownToolDefinition[]>(propTools ?? KNOWN_WORKFLOW_TOOLS);

  useEffect(() => {
    if (propTools && propTools.length > 0) {
      setAvailableTools(propTools);
    }
  }, [propTools]);

  const [name, setName] = useState(state.name);
  const [description, setDescription] = useState(state.description ?? "");
  const [entryText, setEntryText] = useState(state.entry_text ?? "");
  const [makeEntry, setMakeEntry] = useState(isEntry);
  const [allowExit, setAllowExit] = useState(state.allow_exit !== false);

  const initialRestricted = Boolean(state.allowed_tools && state.allowed_tools.length > 0);
  const [restrictTools, setRestrictTools] = useState(initialRestricted);
  const [selectedTools, setSelectedTools] = useState<Set<string>>(
    new Set(state.allowed_tools ?? [])
  );

  const [reminders, setReminders] = useState<WorkflowReminderConfig[]>(
    state.reminders ? state.reminders.map((r) => ({ ...r })) : []
  );

  const [monitors, setMonitors] = useState<WorkflowMonitorConfig[]>(
    state.monitors ? state.monitors.map((m) => ({ ...m })) : []
  );

  const [activeTab, setActiveTab] = useState<"general" | "monitors">("general");

  // Monitor form state inside Monitors tab
  const [showMonitorForm, setShowMonitorForm] = useState(false);
  const [editingMonitorIdx, setEditingMonitorIdx] = useState<number | null>(null);
  const [monitorSelectedPreset, setMonitorSelectedPreset] = useState("");
  const [monitorName, setMonitorName] = useState("");
  const [monitorNudge, setMonitorNudge] = useState("");
  const [monitorInterval, setMonitorInterval] = useState(30);
  const [monitorCooldown, setMonitorCooldown] = useState(120);
  const [monitorInstructions, setMonitorInstructions] = useState("");
  const [monitorQuestionType, setMonitorQuestionType] = useState<"noul" | "choice" | "score">("noul");
  const [monitorOperator, setMonitorOperator] = useState("gte");
  const [monitorThreshold, setMonitorThreshold] = useState("0.8");
  const [monitorTrailingCount, setMonitorTrailingCount] = useState(6);
  const [monitorMaxAlerts, setMonitorMaxAlerts] = useState(0);
  const [monitorShowAdvanced, setMonitorShowAdvanced] = useState(false);
  const [monitorFormError, setMonitorFormError] = useState<string | null>(null);

  const [goal, setGoal] = useState<string>(state.goal ?? "");
  const [showGoalEditor, setShowGoalEditor] = useState(false);

  // Sub-reminder dialog or inline editing
  const [editingReminderIdx, setEditingReminderIdx] = useState<number | null>(null);
  const [reminderType, setReminderType] = useState<"one_shot" | "recurring">("one_shot");
  const [reminderDelay, setReminderDelay] = useState(300);
  const [reminderInterval, setReminderInterval] = useState(300);
  const [reminderBody, setReminderBody] = useState("");
  const [reminderSubject, setReminderSubject] = useState("");
  const [reminderError, setReminderError] = useState<string | null>(null);
  const [showReminderForm, setShowReminderForm] = useState(false);

  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const toggleTool = (toolId: string) => {
    setSelectedTools((prev) => {
      const next = new Set(prev);
      if (next.has(toolId)) {
        next.delete(toolId);
      } else {
        next.add(toolId);
      }
      return next;
    });
  };

  const openAddReminder = () => {
    setEditingReminderIdx(null);
    setReminderType("one_shot");
    setReminderDelay(300);
    setReminderInterval(300);
    setReminderBody("");
    setReminderSubject("");
    setReminderError(null);
    setShowReminderForm(true);
  };

  const openEditReminder = (idx: number) => {
    const r = reminders[idx];
    if (!r) return;
    setEditingReminderIdx(idx);
    setReminderType(r.type);
    setReminderDelay(r.delay_seconds);
    setReminderInterval(r.interval_seconds ?? r.delay_seconds ?? 300);
    setReminderBody(r.body);
    setReminderSubject(r.subject ?? "");
    setReminderError(null);
    setShowReminderForm(true);
  };

  const saveReminder = (e: FormEvent) => {
    e.preventDefault();
    if (!reminderBody.trim()) {
      setReminderError("Reminder body text is required");
      return;
    }
    if (reminderDelay < 1) {
      setReminderError("Delay must be at least 1 second");
      return;
    }
    if (reminderType === "recurring" && reminderInterval < 1) {
      setReminderError("Interval must be at least 1 second");
      return;
    }

    const config: WorkflowReminderConfig = {
      id: editingReminderIdx !== null ? reminders[editingReminderIdx]?.id ?? secureUuid() : secureUuid(),
      type: reminderType,
      delay_seconds: reminderDelay,
      body: reminderBody.trim(),
      ...(reminderSubject.trim() ? { subject: reminderSubject.trim() } : {}),
      ...(reminderType === "recurring" ? { interval_seconds: reminderInterval } : {}),
    };

    if (editingReminderIdx !== null) {
      setReminders((prev) => prev.map((r, i) => (i === editingReminderIdx ? config : r)));
    } else {
      setReminders((prev) => [...prev, config]);
    }
    setShowReminderForm(false);
  };

  const removeReminder = (idx: number) => {
    setReminders((prev) => prev.filter((_, i) => i !== idx));
  };

  const openAddMonitor = () => {
    setEditingMonitorIdx(null);
    setMonitorSelectedPreset("");
    setMonitorName("");
    setMonitorNudge("");
    setMonitorInterval(30);
    setMonitorCooldown(120);
    setMonitorInstructions("");
    setMonitorQuestionType("noul");
    setMonitorOperator("gte");
    setMonitorThreshold("0.8");
    setMonitorTrailingCount(6);
    setMonitorMaxAlerts(0);
    setMonitorShowAdvanced(false);
    setMonitorFormError(null);
    setShowMonitorForm(true);
  };

  const openEditMonitor = (idx: number) => {
    const m = monitors[idx];
    if (!m) return;
    setEditingMonitorIdx(idx);
    setMonitorSelectedPreset("");
    setMonitorName(m.name);
    setMonitorNudge(m.nudge_template);
    setMonitorInterval(m.interval_seconds ?? 30);
    setMonitorCooldown(m.cooldown_seconds ?? 120);
    setMonitorInstructions(m.question_instructions);
    setMonitorQuestionType(m.question_type ?? "noul");
    setMonitorOperator(m.condition_operator ?? "gte");
    setMonitorThreshold(m.condition_threshold ?? "0.8");
    setMonitorTrailingCount(m.trailing_messages_count ?? 6);
    setMonitorMaxAlerts(m.max_alerts ?? 0);
    setMonitorShowAdvanced(false);
    setMonitorFormError(null);
    setShowMonitorForm(true);
  };

  const handleMonitorPresetSelect = (presetId: string) => {
    setMonitorSelectedPreset(presetId);
    if (!presetId) return;
    const p = getMonitorPresetById(presetId);
    if (p) {
      setMonitorName(p.payload.name);
      setMonitorNudge(p.payload.nudge_template);
      setMonitorInterval(p.payload.interval_seconds ?? 30);
      setMonitorCooldown(p.payload.cooldown_seconds ?? 120);
      setMonitorInstructions(p.payload.question_instructions);
      setMonitorQuestionType(p.payload.question_type);
      setMonitorOperator(p.payload.condition_operator);
      setMonitorThreshold(p.payload.condition_threshold);
      setMonitorTrailingCount(p.payload.trailing_messages_count ?? 6);
      setMonitorMaxAlerts(p.payload.max_alerts ?? 0);
    }
  };

  const saveMonitor = (e: FormEvent) => {
    e.preventDefault();
    if (!monitorName.trim()) {
      setMonitorFormError("Monitor name is required");
      return;
    }
    if (!monitorNudge.trim()) {
      setMonitorFormError("Nudge message is required");
      return;
    }
    if (!monitorInstructions.trim()) {
      setMonitorFormError("Question instructions are required");
      return;
    }

    const config: WorkflowMonitorConfig = {
      id: editingMonitorIdx !== null ? monitors[editingMonitorIdx]?.id ?? secureUuid() : secureUuid(),
      name: monitorName.trim(),
      nudge_template: monitorNudge.trim(),
      interval_seconds: monitorInterval,
      cooldown_seconds: monitorCooldown,
      question_instructions: monitorInstructions.trim(),
      question_type: monitorQuestionType,
      condition_operator: monitorOperator,
      condition_threshold: monitorThreshold,
      trailing_messages_count: monitorTrailingCount,
      max_alerts: monitorMaxAlerts,
      fallback_mode: "skip",
    };

    if (editingMonitorIdx !== null) {
      setMonitors((prev) => prev.map((m, i) => (i === editingMonitorIdx ? config : m)));
    } else {
      setMonitors((prev) => [...prev, config]);
    }
    setShowMonitorForm(false);
  };

  const removeMonitor = (idx: number) => {
    setMonitors((prev) => prev.filter((_, i) => i !== idx));
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      setError("State name is required");
      return;
    }

    setSaving(true);
    setError(null);
    try {
      const updated: WorkflowState = {
        ...state,
        name: name.trim(),
        description: description.trim(),
        entry_text: entryText,
        goal: goal.trim(),
        allowed_tools: restrictTools ? Array.from(selectedTools) : [],
        reminders,
        monitors,
        allow_exit: allowExit,
      };
      await onSave(updated, makeEntry);
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save state configuration");
    } finally {
      setSaving(false);
    }
  };

  return (
    <DialogShell
      open={true}
      title={`Configure State: ${state.name}`}
      titleId="state-config-dialog-title"
      description="Edit state properties, tool restrictions, reminders, and entry prompts."
      onClose={onClose}
      closeLabel="Close state configuration dialog"
      className="message-dialog state-config-dialog"
    >
      <form onSubmit={(e) => void handleSubmit(e)} className="state-config-form">
        {error ? (
          <div
            className="alert alert-danger"
            style={{
              color: "#f87171",
              backgroundColor: "rgba(127, 29, 29, 0.4)",
              border: "1px solid #991b1b",
              borderRadius: "6px",
              padding: "8px 12px",
              fontSize: "13px",
              marginBottom: "12px",
            }}
          >
            {error}
          </div>
        ) : null}

        {/* State identity */}
        <div className="form-group" style={{ marginBottom: "0.85rem" }}>
          <label htmlFor="state-name-input" style={{ display: "block", marginBottom: "0.3rem", fontWeight: 600 }}>
            State Name *
          </label>
          <input
            id="state-name-input"
            type="text"
            className="form-control"
            value={name}
            onChange={(e) => setName(e.target.value)}
            required
            autoFocus
          />
        </div>

        <div className="form-group" style={{ marginBottom: "0.85rem" }}>
          <label htmlFor="state-desc-input" style={{ display: "block", marginBottom: "0.3rem", fontWeight: 600 }}>
            Description (optional)
          </label>
          <input
            id="state-desc-input"
            type="text"
            className="form-control"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="Brief purpose of this step"
          />
        </div>

        {/* Entry state switch */}
        <div className="toggle-switch-row">
          <div className="toggle-switch-label">
            <span className="toggle-switch-title">Designate as Entry State (Workflow Start)</span>
            <span className="toggle-switch-sub">The entry state is where agents begin executing this workflow.</span>
          </div>
          <label className="switch-toggle" aria-label="Designate as Entry State (Workflow Start)">
            <input
              type="checkbox"
              checked={makeEntry}
              onChange={(e) => setMakeEntry(e.target.checked)}
            />
            <span className="switch-slider" />
          </label>
        </div>

        {/* Entry Text */}
        <div className="form-group" style={{ marginBottom: "1rem" }}>
          <label htmlFor="state-entry-text" style={{ display: "block", marginBottom: "0.3rem", fontWeight: 600 }}>
            Agent Instructions (Entry Text)
          </label>
          <textarea
            id="state-entry-text"
            className="form-control"
            rows={3}
            value={entryText}
            onChange={(e) => setEntryText(e.target.value)}
            placeholder="Plain-English instructions injected to agent upon entering this state..."
          />
        </div>

        {/* Exit Permission switch */}
        <div className="toggle-switch-row">
          <div className="toggle-switch-label">
            <span className="toggle-switch-title">Allow Exit / Terminal State</span>
            <span className="toggle-switch-sub">When enabled, the agent is allowed to complete or finish the workflow from this state.</span>
          </div>
          <label className="switch-toggle" aria-label="Allow Exit / Terminal State">
            <input
              type="checkbox"
              checked={allowExit}
              onChange={(e) => setAllowExit(e.target.checked)}
            />
            <span className="switch-slider" />
          </label>
        </div>

        {/* Tool Restrictions section */}
        <div className="workflow-tool-section" style={{ marginBottom: "1.25rem", borderTop: "1px solid var(--border-color, #333)", paddingTop: "0.75rem" }}>
          <button
            type="button"
            className="goal-toggle workflow-tool-master-toggle"
            aria-pressed={!restrictTools}
            data-testid="allow-all-tools-toggle"
            onClick={() => {
              const nextAllowAll = restrictTools; // if restrictTools is true, next is allow all
              setRestrictTools(!nextAllowAll);
              if (nextAllowAll) {
                setSelectedTools(new Set());
              } else if (selectedTools.size === 0) {
                setSelectedTools(new Set(availableTools.map((t) => t.id)));
              }
            }}
          >
            <div className="workflow-master-toggle-text">
              <span className="goal-toggle-label">Allow all tools (unrestricted)</span>
              <span className="workflow-toggle-subtext">Workers in this state have access to all available tools</span>
            </div>
            <span className="goal-toggle-track" aria-hidden="true">
              <span className="goal-toggle-thumb">
                <span className="goal-toggle-state">{!restrictTools ? "ON" : "OFF"}</span>
              </span>
            </span>
          </button>

          {restrictTools ? (
            <div className="workflow-tool-picker">
              <div className="workflow-tool-picker-toolbar">
                <span className="workflow-tool-picker-title">
                  Allowed Tools ({selectedTools.size} of {availableTools.length})
                </span>
                <div className="workflow-tool-picker-actions">
                  <button
                    type="button"
                    className="secondary workflow-tool-action-btn"
                    onClick={() => setSelectedTools(new Set(availableTools.map((t) => t.id)))}
                  >
                    Select All
                  </button>
                  <button
                    type="button"
                    className="secondary workflow-tool-action-btn"
                    onClick={() => setSelectedTools(new Set())}
                  >
                    Deselect All
                  </button>
                </div>
              </div>

              <div className="workflow-tool-grid">
                {availableTools.map((tool) => {
                  const isSelected = selectedTools.has(tool.id);
                  return (
                    <button
                      type="button"
                      key={tool.id}
                      className={`workflow-tool-tile ${isSelected ? "selected" : ""}`}
                      aria-pressed={isSelected}
                      data-testid={`tool-toggle-${tool.id}`}
                      onClick={() => toggleTool(tool.id)}
                    >
                      <div className="workflow-tool-tile-header">
                        <code className="workflow-tool-tile-name">{tool.name}</code>
                        <span className={`workflow-tool-tile-pill ${isSelected ? "active" : ""}`}>
                          {isSelected ? (
                            <>
                              <i className="fa-solid fa-check" aria-hidden="true" /> ALLOWED
                            </>
                          ) : (
                            "OFF"
                          )}
                        </span>
                      </div>
                      <p className="workflow-tool-tile-desc">{tool.description}</p>
                    </button>
                  );
                })}
              </div>
            </div>
          ) : null}
        </div>

        {/* Workflow State Goal section */}
        <div className="form-section" style={{ marginBottom: "1.25rem", borderTop: "1px solid var(--border-color, #333)", paddingTop: "0.75rem" }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "0.5rem" }}>
            <div style={{ fontWeight: 600 }}>Workflow State Goal</div>
            {!goal ? (
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                onClick={() => setShowGoalEditor(true)}
              >
                <i className="fa-solid fa-flag" aria-hidden="true" /> Configure Goal
              </button>
            ) : null}
          </div>

          {!goal ? (
            <p className="text-muted" style={{ fontSize: "0.85em", margin: "0" }}>
              No persistent goal configured. Workers in this state will operate without a locked workflow goal.
            </p>
          ) : (
            <div className="state-goal-preview-box" style={{ padding: "8px 12px", background: "rgba(255,255,255,0.04)", border: "1px solid var(--border-color, #444)", borderRadius: "4px" }}>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: "8px" }}>
                <div style={{ flex: 1, whiteSpace: "pre-wrap", fontSize: "0.85em", lineHeight: 1.4 }}>
                  {goal}
                </div>
                <div style={{ display: "flex", gap: "6px", flexShrink: 0 }}>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    onClick={() => setShowGoalEditor(true)}
                  >
                    <i className="fa-solid fa-pen" aria-hidden="true" /> Edit Goal
                  </button>
                  <button
                    type="button"
                    className="btn btn-danger btn-sm"
                    onClick={() => setGoal("")}
                  >
                    <i className="fa-solid fa-trash" aria-hidden="true" /> Clear Goal
                  </button>
                </div>
              </div>
            </div>
          )}
        </div>

        {/* Reminders section */}
        <div className="form-section" style={{ marginBottom: "1.25rem", borderTop: "1px solid var(--border-color, #333)", paddingTop: "0.75rem" }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "0.5rem" }}>
            <div style={{ fontWeight: 600 }}>Scheduled Reminders</div>
            <button
              type="button"
              className="btn btn-secondary btn-sm"
              onClick={openAddReminder}
            >
              <i className="fa-solid fa-plus" aria-hidden="true" /> Add Reminder
            </button>
          </div>

          {reminders.length === 0 ? (
            <p className="text-muted" style={{ fontSize: "0.85em", margin: "0" }}>No reminders configured for this state.</p>
          ) : (
            <div className="reminders-list" style={{ display: "flex", flexDirection: "column", gap: "0.5rem" }}>
              {reminders.map((r, idx) => (
                <div key={r.id || idx} style={{ display: "flex", justifyContent: "space-between", alignItems: "center", padding: "6px 8px", background: "rgba(255,255,255,0.04)", borderRadius: "4px" }}>
                  <div>
                    <span style={{ fontWeight: 600, fontSize: "0.9em" }}>
                      {r.type === "recurring" ? `Recurring (${r.interval_seconds ?? r.delay_seconds}s)` : `One-shot (${r.delay_seconds}s)`}:
                    </span>{" "}
                    <span style={{ fontSize: "0.85em" }}>{r.body}</span>
                  </div>
                  <div style={{ display: "flex", gap: "4px" }}>
                    <button type="button" className="btn-icon" onClick={() => openEditReminder(idx)} title="Edit reminder">
                      <i className="fa-solid fa-pen" aria-hidden="true" />
                    </button>
                    <button type="button" className="btn-icon btn-danger" onClick={() => removeReminder(idx)} title="Delete reminder">
                      <i className="fa-solid fa-trash" aria-hidden="true" />
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Inline Reminder Edit Form */}
        {showReminderForm ? (
          <div className="reminder-inline-form" style={{ padding: "0.75rem", background: "rgba(0,0,0,0.2)", border: "1px solid var(--border-color, #444)", borderRadius: "4px", marginBottom: "1rem" }}>
            <div style={{ fontWeight: 600, marginBottom: "0.5rem" }}>
              {editingReminderIdx !== null ? "Edit Reminder" : "New Reminder"}
            </div>
            {reminderError ? (
              <div
                className="alert alert-danger"
                style={{
                  color: "#f87171",
                  backgroundColor: "rgba(127, 29, 29, 0.4)",
                  border: "1px solid #991b1b",
                  borderRadius: "6px",
                  padding: "8px 12px",
                  fontSize: "13px",
                  marginBottom: "12px",
                }}
              >
                {reminderError}
              </div>
            ) : null}
            <div style={{ marginBottom: "0.5rem" }}>
              <div className="segmented-toggle-group" role="group" aria-label="Reminder Type">
                <button
                  type="button"
                  className={`segmented-toggle-btn ${reminderType === "one_shot" ? "active" : ""}`}
                  onClick={() => setReminderType("one_shot")}
                >
                  One-shot
                </button>
                <button
                  type="button"
                  className={`segmented-toggle-btn ${reminderType === "recurring" ? "active" : ""}`}
                  onClick={() => setReminderType("recurring")}
                >
                  Recurring
                </button>
              </div>
            </div>
            <div style={{ display: "flex", gap: "0.5rem", marginBottom: "0.5rem" }}>
              <div style={{ flex: 1 }}>
                <label style={{ fontSize: "0.8em", display: "block" }}>Delay (seconds)</label>
                <input
                  type="number"
                  min={1}
                  className="form-control form-control-sm"
                  value={reminderDelay}
                  onChange={(e) => setReminderDelay(Number(e.target.value))}
                />
              </div>
              {reminderType === "recurring" ? (
                <div style={{ flex: 1 }}>
                  <label style={{ fontSize: "0.8em", display: "block" }}>Interval (seconds)</label>
                  <input
                    type="number"
                    min={1}
                    className="form-control form-control-sm"
                    value={reminderInterval}
                    onChange={(e) => setReminderInterval(Number(e.target.value))}
                  />
                </div>
              ) : null}
            </div>
            <div style={{ marginBottom: "0.5rem" }}>
              <label style={{ fontSize: "0.8em", display: "block" }}>Subject (optional)</label>
              <input
                type="text"
                className="form-control form-control-sm"
                value={reminderSubject}
                onChange={(e) => setReminderSubject(e.target.value)}
                placeholder="Subject prefix"
              />
            </div>
            <div style={{ marginBottom: "0.5rem" }}>
              <label style={{ fontSize: "0.8em", display: "block" }}>Message Body *</label>
              <textarea
                className="form-control form-control-sm"
                rows={2}
                value={reminderBody}
                onChange={(e) => setReminderBody(e.target.value)}
                placeholder="Reminder message text sent to agent..."
                required
              />
            </div>
            <div style={{ display: "flex", justifyContent: "flex-end", gap: "0.5rem" }}>
              <button type="button" className="btn btn-secondary btn-sm" onClick={() => setShowReminderForm(false)}>
                Cancel
              </button>
              <button type="button" className="btn btn-primary btn-sm" onClick={(e) => saveReminder(e)}>
                Save Reminder
              </button>
            </div>
          </div>
        ) : null}

        {/* Action buttons */}
        <div className="dialog-actions workflow-dialog-actions" style={{ marginTop: "1rem" }}>
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={saving}>
            {saving ? "Saving..." : "Save State Settings"}
          </button>
        </div>
      </form>

      {/* Goal Editor Dialog */}
      {showGoalEditor ? (
        <GoalEditorDialog
          open={showGoalEditor}
          peer={`State: ${name || state.name}`}
          goalState={{
            ...DEFAULT_GOAL_STATE,
            enabled: Boolean(goal),
            goal: goal || "",
          }}
          onClose={() => setShowGoalEditor(false)}
          onSave={(payload) => {
            if (payload.action === "set" && payload.goal) {
              setGoal(payload.goal);
            } else {
              setGoal("");
            }
            setShowGoalEditor(false);
          }}
        />
      ) : null}
    </DialogShell>
  );
}
