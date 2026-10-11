import { useState, type ReactElement, type FormEvent } from "react";
import type { WorkflowState, WorkflowReminderConfig, KnownToolDefinition } from "../domain/workflow-states";
import { KNOWN_WORKFLOW_TOOLS } from "../domain/workflow-states";
import { secureUuid } from "../platform/secure-uuid";
import { DialogShell } from "./DialogShell";

export interface StateConfigModalProps {
  state: WorkflowState;
  isEntry: boolean;
  onClose: () => void;
  onSave: (updatedState: WorkflowState, makeEntry: boolean) => Promise<void>;
}

export function StateConfigModal({ state, isEntry, onClose, onSave }: StateConfigModalProps): ReactElement {
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
        allowed_tools: restrictTools ? Array.from(selectedTools) : [],
        reminders,
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

        {/* Entry state checkbox */}
        <div className="form-check-group" style={{ marginBottom: "1rem" }}>
          <label style={{ display: "flex", alignItems: "center", gap: "0.5rem", cursor: "pointer", fontWeight: 500 }}>
            <input
              type="checkbox"
              checked={makeEntry}
              onChange={(e) => setMakeEntry(e.target.checked)}
            />
            <span>Designate as Entry State (Workflow Start)</span>
          </label>
          <small className="text-muted" style={{ display: "block", marginLeft: "1.5rem" }}>
            The entry state is where agents begin executing this workflow.
          </small>
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

        {/* Exit Permission */}
        <div className="form-check-group" style={{ marginBottom: "1rem" }}>
          <label style={{ display: "flex", alignItems: "center", gap: "0.5rem", cursor: "pointer", fontWeight: 500 }}>
            <input
              type="checkbox"
              checked={allowExit}
              onChange={(e) => setAllowExit(e.target.checked)}
            />
            <span>Allow Exit / Terminal State</span>
          </label>
          <small className="text-muted" style={{ display: "block", marginLeft: "1.5rem" }}>
            When enabled, the agent is allowed to complete or finish the workflow from this state.
          </small>
        </div>

        {/* Allowed Tools */}
        <div className="form-section" style={{ marginBottom: "1.25rem", borderTop: "1px solid var(--border-color, #333)", paddingTop: "0.75rem" }}>
          <div style={{ fontWeight: 600, marginBottom: "0.5rem" }}>Tool Restrictions</div>
          <div style={{ display: "flex", gap: "1rem", marginBottom: "0.75rem" }}>
            <label style={{ display: "flex", alignItems: "center", gap: "0.4rem", cursor: "pointer" }}>
              <input
                type="radio"
                name="tool-mode"
                checked={!restrictTools}
                onChange={() => setRestrictTools(false)}
              />
              <span>Allow all tools</span>
            </label>
            <label style={{ display: "flex", alignItems: "center", gap: "0.4rem", cursor: "pointer" }}>
              <input
                type="radio"
                name="tool-mode"
                checked={restrictTools}
                onChange={() => setRestrictTools(true)}
              />
              <span>Restrict tools</span>
            </label>
          </div>

          {restrictTools ? (
            <div className="tools-picker-grid" style={{ maxHeight: "160px", overflowY: "auto", border: "1px solid var(--border-color, #444)", borderRadius: "4px", padding: "0.5rem" }}>
              {KNOWN_WORKFLOW_TOOLS.map((t: KnownToolDefinition) => (
                <label key={t.id} style={{ display: "flex", alignItems: "center", gap: "0.5rem", padding: "3px 0", cursor: "pointer" }}>
                  <input
                    type="checkbox"
                    checked={selectedTools.has(t.id)}
                    onChange={() => toggleTool(t.id)}
                  />
                  <span style={{ fontFamily: "monospace", fontSize: "0.9em" }}>{t.name}</span>
                  <span className="text-muted" style={{ fontSize: "0.8em" }}>— {t.description}</span>
                </label>
              ))}
            </div>
          ) : null}
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
            <div style={{ display: "flex", gap: "0.75rem", marginBottom: "0.5rem" }}>
              <label style={{ display: "flex", alignItems: "center", gap: "0.3rem" }}>
                <input
                  type="radio"
                  name="rem-type"
                  checked={reminderType === "one_shot"}
                  onChange={() => setReminderType("one_shot")}
                />
                <span>One-shot</span>
              </label>
              <label style={{ display: "flex", alignItems: "center", gap: "0.3rem" }}>
                <input
                  type="radio"
                  name="rem-type"
                  checked={reminderType === "recurring"}
                  onChange={() => setReminderType("recurring")}
                />
                <span>Recurring</span>
              </label>
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
    </DialogShell>
  );
}
