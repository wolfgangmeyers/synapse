import { useState, useEffect, type ReactElement } from "react";
import type { RouterHttpClient } from "../transport/http-client";
import {
  type WorkflowState,
  type WorkflowStateInput,
  type WorkflowReminderConfig,
  type WorkflowReminderType,
  type WorkflowTransitionConfig,
  type KnownToolDefinition,
  KNOWN_WORKFLOW_TOOLS,
} from "../domain/workflow-states";
import { secureUuid } from "../platform/secure-uuid";
import { DialogShell } from "./DialogShell";

export interface WorkflowStatesPageProps {
  client: RouterHttpClient;
  onNavigateHome?: () => void;
  onBack?: () => void;
}

function formatDuration(seconds: number): string {
  if (seconds >= 60 && seconds % 60 === 0) {
    const mins = Math.floor(seconds / 60);
    if (mins >= 60 && mins % 60 === 0) {
      return `${mins / 60}h`;
    }
    return `${mins}m`;
  }
  return `${seconds}s`;
}

export function WorkflowStatesPage({ client, onNavigateHome, onBack }: WorkflowStatesPageProps): ReactElement {
  const [states, setStates] = useState<WorkflowState[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>(undefined);
  const [editingState, setEditingState] = useState<WorkflowState | undefined>(undefined);
  const [isCreating, setIsCreating] = useState(false);
  const [formName, setFormName] = useState("");
  const [formDescription, setFormDescription] = useState("");
  const [formEntryText, setFormEntryText] = useState("");
  const [formGoal, setFormGoal] = useState("");
  const [formReminders, setFormReminders] = useState<WorkflowReminderConfig[]>([]);
  const [formAllToolsAllowed, setFormAllToolsAllowed] = useState(true);
  const [formAllowedTools, setFormAllowedTools] = useState<string[]>([]);
  const [formTransitions, setFormTransitions] = useState<WorkflowTransitionConfig[]>([]);
  const [formAllowExit, setFormAllowExit] = useState(true);
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | undefined>(undefined);
  const [deleteConfirmId, setDeleteConfirmId] = useState<string | undefined>(undefined);
  const [tools, setTools] = useState<KnownToolDefinition[]>(KNOWN_WORKFLOW_TOOLS);

  const fetchStates = async () => {
    try {
      setLoading(true);
      setError(undefined);
      const [resStates, resTools] = await Promise.all([
        client.workflowStates(),
        typeof client.workflowTools === "function" ? client.workflowTools().catch(() => null) : Promise.resolve(null),
      ]);
      setStates(resStates.workflow_states);
      if (resTools?.tools && Array.isArray(resTools.tools)) {
        setTools(resTools.tools);
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to load workflow states");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void fetchStates();
  }, []);

  const openCreate = () => {
    setEditingState(undefined);
    setFormName("");
    setFormDescription("");
    setFormEntryText("");
    setFormGoal("");
    setFormReminders([]);
    setFormAllToolsAllowed(true);
    setFormAllowedTools([]);
    setFormTransitions([]);
    setFormAllowExit(true);
    setFormError(undefined);
    setIsCreating(true);
  };

  const openEdit = (state: WorkflowState) => {
    setEditingState(state);
    setFormName(state.name);
    setFormDescription(state.description);
    setFormEntryText(state.entry_text);
    setFormGoal(state.goal ?? "");
    setFormReminders(state.reminders ? state.reminders.map((r) => ({ ...r })) : []);
    const hasRestrictions = Boolean(state.allowed_tools && state.allowed_tools.length > 0);
    setFormAllToolsAllowed(!hasRestrictions);
    setFormAllowedTools(hasRestrictions ? [...(state.allowed_tools ?? [])] : []);
    setFormTransitions(state.transitions ? state.transitions.map((t) => ({ ...t })) : []);
    setFormAllowExit(state.allow_exit !== false);
    setFormError(undefined);
    setIsCreating(false);
  };

  const closeForm = () => {
    setEditingState(undefined);
    setIsCreating(false);
    setFormError(undefined);
  };

  const [isReminderDialogOpen, setIsReminderDialogOpen] = useState(false);
  const [editingReminderIndex, setEditingReminderIndex] = useState<number | undefined>(undefined);
  const [reminderDraftSubject, setReminderDraftSubject] = useState("");
  const [reminderDraftBody, setReminderDraftBody] = useState("");
  const [reminderDraftDelay, setReminderDraftDelay] = useState(300);
  const [reminderDraftRecurring, setReminderDraftRecurring] = useState(false);
  const [reminderDraftInterval, setReminderDraftInterval] = useState(300);
  const [reminderDraftError, setReminderDraftError] = useState("");

  const openAddReminder = () => {
    setEditingReminderIndex(undefined);
    setReminderDraftSubject("");
    setReminderDraftBody("");
    setReminderDraftDelay(300);
    setReminderDraftRecurring(false);
    setReminderDraftInterval(300);
    setReminderDraftError("");
    setIsReminderDialogOpen(true);
  };

  const openEditReminder = (index: number) => {
    const reminder = formReminders[index];
    if (!reminder) return;
    setEditingReminderIndex(index);
    setReminderDraftSubject(reminder.subject ?? "");
    setReminderDraftBody(reminder.body);
    setReminderDraftDelay(reminder.delay_seconds);
    setReminderDraftRecurring(reminder.type === "recurring");
    setReminderDraftInterval(reminder.interval_seconds ?? reminder.delay_seconds ?? 300);
    setReminderDraftError("");
    setIsReminderDialogOpen(true);
  };

  const handleSaveReminder = (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = reminderDraftBody.trim();
    if (!trimmed) {
      setReminderDraftError("Enter reminder text.");
      return;
    }
    if (typeof reminderDraftDelay !== "number" || isNaN(reminderDraftDelay) || reminderDraftDelay < 1) {
      setReminderDraftError("Enter a delay of at least 1 second.");
      return;
    }
    if (reminderDraftRecurring && (typeof reminderDraftInterval !== "number" || isNaN(reminderDraftInterval) || reminderDraftInterval < 1)) {
      setReminderDraftError("Enter an interval of at least 1 second.");
      return;
    }

    const reminderConfig: WorkflowReminderConfig = {
      id: editingReminderIndex !== undefined ? formReminders[editingReminderIndex]?.id ?? secureUuid() : secureUuid(),
      type: reminderDraftRecurring ? "recurring" : "one_shot",
      delay_seconds: reminderDraftDelay,
      body: trimmed,
      ...(reminderDraftSubject.trim() ? { subject: reminderDraftSubject.trim() } : {}),
      ...(reminderDraftRecurring ? { interval_seconds: reminderDraftInterval } : {}),
    };

    if (editingReminderIndex !== undefined) {
      setFormReminders((prev) =>
        prev.map((r, i) => (i === editingReminderIndex ? reminderConfig : r))
      );
    } else {
      setFormReminders((prev) => [...prev, reminderConfig]);
    }

    setIsReminderDialogOpen(false);
  };

  const removeReminder = (index: number) => {
    setFormReminders((prev) => prev.filter((_, i) => i !== index));
  };

  const addTransition = () => {
    setFormTransitions((prev) => [...prev, { target_state_id: "", description: "" }]);
  };

  const updateTransition = (index: number, field: "target_state_id" | "description", value: string) => {
    setFormTransitions((prev) =>
      prev.map((t, i) => (i === index ? { ...t, [field]: value } : t))
    );
  };

  const removeTransition = (index: number) => {
    setFormTransitions((prev) => prev.filter((_, i) => i !== index));
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!formName.trim()) {
      setFormError("State name is required");
      return;
    }

    for (let i = 0; i < formReminders.length; i++) {
      const r = formReminders[i];
      if (!r) continue;
      if (!r.body.trim()) {
        setFormError(`Reminder #${i + 1} text is required`);
        return;
      }
      if (typeof r.delay_seconds !== "number" || isNaN(r.delay_seconds) || r.delay_seconds < 0) {
        setFormError(`Reminder #${i + 1} delay must be a non-negative number`);
        return;
      }
      if (r.type === "recurring") {
        if (typeof r.interval_seconds !== "number" || isNaN(r.interval_seconds) || r.interval_seconds <= 0) {
          setFormError(`Reminder #${i + 1} interval must be greater than 0`);
          return;
        }
      }
    }

    const validTransitions: WorkflowTransitionConfig[] = [];
    const seenTargets = new Set<string>();
    for (let i = 0; i < formTransitions.length; i++) {
      const tr = formTransitions[i];
      if (!tr) continue;
      const targetId = tr.target_state_id.trim();
      if (!targetId) {
        setFormError(`Transition #${i + 1} must select a target state`);
        return;
      }
      if (editingState && targetId === editingState.id) {
        setFormError(`Transition #${i + 1} cannot point to the current state (self-transition is not permitted)`);
        return;
      }
      if (seenTargets.has(targetId)) {
        setFormError(`Duplicate transition target detected for state: ${targetId}`);
        return;
      }
      seenTargets.add(targetId);
      validTransitions.push({
        target_state_id: targetId,
        description: tr.description.trim(),
      });
    }

    setSaving(true);
    setFormError(undefined);
    const input: WorkflowStateInput = {
      name: formName.trim(),
      description: formDescription.trim(),
      entry_text: formEntryText,
      goal: formGoal.trim(),
      allowed_tools: formAllToolsAllowed ? [] : formAllowedTools,
      transitions: validTransitions,
      allow_exit: formAllowExit,
      reminders: formReminders.map((r) => ({
        id: r.id,
        type: r.type,
        delay_seconds: Number(r.delay_seconds),
        body: r.body.trim(),
        ...(r.subject?.trim() ? { subject: r.subject.trim() } : {}),
        ...(r.type === "recurring" && r.interval_seconds !== undefined
          ? { interval_seconds: Number(r.interval_seconds) }
          : {}),
      })),
    };

    try {
      if (editingState) {
        await client.updateWorkflowState(editingState.id, input);
      } else {
        await client.createWorkflowState(input);
      }
      closeForm();
      await fetchStates();
    } catch (err: unknown) {
      setFormError(err instanceof Error ? err.message : "Failed to save workflow state");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (id: string) => {
    try {
      await client.deleteWorkflowState(id);
      setDeleteConfirmId(undefined);
      await fetchStates();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete workflow state");
    }
  };

  const handleBack = () => {
    if (onBack) {
      onBack();
    } else if (onNavigateHome) {
      onNavigateHome();
    }
  };

  return (
    <div className="workflows-page">
      <header className="workflows-header">
        <div className="workflows-nav">
          <button type="button" className="workflows-back-btn" onClick={handleBack}>
            <i className="fa-solid fa-arrow-left" aria-hidden="true" /> Dashboard
          </button>
          <div className="workflows-title">
            <h2>Workflow States</h2>
            <span className="badge">{states.length}</span>
          </div>
        </div>
        <button type="button" className="btn btn-primary" onClick={openCreate}>
          <i className="fa-solid fa-plus" aria-hidden="true" /> New State
        </button>
      </header>

      {error ? (
        <div className="alert alert-danger" role="alert">
          {error}
        </div>
      ) : null}

      <div className="workflows-content">
        {loading ? (
          <div className="workflows-loading">Loading workflow states...</div>
        ) : states.length === 0 ? (
          <div className="workflows-empty">
            <i className="fa-solid fa-diagram-project fa-3x" aria-hidden="true" />
            <p>No workflow states yet.</p>
            <p className="text-muted">Create your first state to define entry prompts for your agents.</p>
            <button type="button" className="btn btn-primary" onClick={openCreate}>
              Create Workflow State
            </button>
          </div>
        ) : (
          <div className="workflow-states-list">
            {states.map((st) => (
              <div key={st.id} className="workflow-state-card">
                <div className="state-card-header">
                  <div className="state-card-title-group">
                    <span className="state-name-badge">{st.name}</span>
                    {st.description ? <p className="state-desc">{st.description}</p> : null}
                  </div>
                  <div className="state-card-actions">
                    <button
                      type="button"
                      className="btn-icon"
                      title="Edit state"
                      aria-label="Edit state"
                      onClick={() => openEdit(st)}
                    >
                      <i className="fa-solid fa-pen-to-square" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="btn-icon btn-danger"
                      title="Delete state"
                      aria-label="Delete state"
                      onClick={() => setDeleteConfirmId(st.id)}
                    >
                      <i className="fa-solid fa-trash" aria-hidden="true" />
                    </button>
                  </div>
                </div>

                <div className="workflow-state-meta-badges">
                  {st.allowed_tools && st.allowed_tools.length > 0 ? (
                    <span
                      className="workflow-tool-badge restricted"
                      title={`Restricted tools: ${st.allowed_tools.join(", ")}`}
                      data-testid={`state-tools-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-wrench" aria-hidden="true" />
                      <span>Tools restricted ({st.allowed_tools.length} allowed)</span>
                    </span>
                  ) : (
                    <span
                      className="workflow-tool-badge unrestricted"
                      title="All tools are available to workers in this state"
                      data-testid={`state-tools-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-wrench" aria-hidden="true" />
                      <span>All tools allowed</span>
                    </span>
                  )}
                  {st.goal ? (
                    <span
                      className="workflow-tool-badge"
                      title={st.goal}
                      data-testid={`state-goal-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-flag" aria-hidden="true" />
                      <span>Goal configured</span>
                    </span>
                  ) : null}
                  {st.allow_exit === false ? (
                    <span
                      className="workflow-tool-badge restricted"
                      title="Workers cannot exit this workflow from this state"
                      data-testid={`state-exit-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-lock" aria-hidden="true" />
                      <span>Exit locked</span>
                    </span>
                  ) : (
                    <span
                      className="workflow-tool-badge unrestricted"
                      title="Workers can exit this workflow from this state"
                      data-testid={`state-exit-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-door-open" aria-hidden="true" />
                      <span>Exit allowed</span>
                    </span>
                  )}
                  {st.transitions && st.transitions.length > 0 ? (
                    <span
                      className="workflow-tool-badge"
                      title={`${st.transitions.length} forward transitions configured`}
                      data-testid={`state-transitions-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-arrow-right-arrow-left" aria-hidden="true" />
                      <span>{st.transitions.length} transition{st.transitions.length > 1 ? "s" : ""}</span>
                    </span>
                  ) : (
                    <span
                      className="workflow-tool-badge"
                      title="No forward transitions configured"
                      data-testid={`state-transitions-badge-${st.id}`}
                    >
                      <i className="fa-solid fa-arrow-right-arrow-left" aria-hidden="true" />
                      <span>0 transitions</span>
                    </span>
                  )}
                </div>

                {st.reminders && st.reminders.length > 0 ? (
                  <div className="workflow-reminder-badges" aria-label="Configured reminders">
                    {st.reminders.map((rem) => {
                      const isRecurring = rem.type === "recurring";
                      const label = isRecurring
                        ? `Recurring every ${formatDuration(rem.interval_seconds ?? rem.delay_seconds)}${
                            rem.delay_seconds > 0 ? ` (starts in ${formatDuration(rem.delay_seconds)})` : ""
                          }`
                        : `One-shot in ${formatDuration(rem.delay_seconds)}`;
                      const fullTitle = `${rem.subject ? `[${rem.subject}] ` : ""}${rem.body}`;

                      return (
                        <span
                          key={rem.id}
                          className={`workflow-reminder-badge${isRecurring ? " recurring" : ""}`}
                          title={fullTitle}
                        >
                          <i
                            className={isRecurring ? "fa-solid fa-arrows-rotate" : "fa-solid fa-clock"}
                            aria-hidden="true"
                          />
                          <span>{label}</span>
                        </span>
                      );
                    })}
                  </div>
                ) : null}

                {st.transitions && st.transitions.length > 0 ? (
                  <div className="workflow-transitions-summary" data-testid={`state-transitions-list-${st.id}`}>
                    <div className="workflow-transitions-summary-title">
                      <i className="fa-solid fa-arrow-right-arrow-left" aria-hidden="true" /> Allowed Transitions:
                    </div>
                    <ul className="workflow-transitions-items">
                      {st.transitions.map((tr, idx) => {
                        const target = states.find((s) => s.id === tr.target_state_id);
                        return (
                          <li key={idx}>
                            <strong>{target ? target.name : tr.target_state_id}</strong>
                            {tr.description ? <span className="target-state-desc"> — {tr.description}</span> : null}
                          </li>
                        );
                      })}
                    </ul>
                  </div>
                ) : null}

                {st.entry_text ? (
                  <div className="state-entry-text">
                    <div className="entry-text-label">Automatic Entry Text:</div>
                    <pre className="entry-text-preview">{st.entry_text}</pre>
                  </div>
                ) : (
                  <div className="state-entry-text text-muted">
                    <em>No automatic entry text configured.</em>
                  </div>
                )}
                <div className="state-footer text-muted">
                  <small>ID: {st.id}</small>
                  <small>Updated: {new Date(st.updated_at).toLocaleString()}</small>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Create / Edit Modal Popup */}
      <DialogShell
        open={isCreating || Boolean(editingState)}
        title={editingState ? "Edit Workflow State" : "New Workflow State"}
        titleId="state-form-title"
        description="Configure entry text, reminders, and metadata for this workflow state."
        onClose={closeForm}
        closeLabel="Close workflow state editor"
        className="message-dialog workflow-state-dialog"
      >
        <form
          onSubmit={(e) => {
            void handleSave(e);
          }}
          className="workflow-state-form"
        >
          {formError ? <div className="alert alert-danger">{formError}</div> : null}
          <div className="form-group">
            <label htmlFor="state-name">State Name *</label>
            <input
              id="state-name"
              type="text"
              className="form-control"
              placeholder="e.g. initial-triage, code-review"
              value={formName}
              onChange={(e) => setFormName(e.target.value)}
              required
            />
          </div>
          <div className="form-group">
            <label htmlFor="state-desc">Description</label>
            <input
              id="state-desc"
              type="text"
              className="form-control"
              placeholder="What is this state for?"
              value={formDescription}
              onChange={(e) => setFormDescription(e.target.value)}
            />
          </div>
          <div className="form-group">
            <label htmlFor="state-entry">Entry Text</label>
            <textarea
              id="state-entry"
              className="form-control"
              rows={5}
              placeholder="Text that automatically posts to the conversation when entering this state..."
              value={formEntryText}
              onChange={(e) => setFormEntryText(e.target.value)}
            />
            <small className="form-text text-muted">
              This text will automatically post to the agent when entering this state.
            </small>
          </div>

          <div className="form-group">
            <label htmlFor="state-goal">Workflow State Goal (optional)</label>
            <textarea
              id="state-goal"
              className="form-control"
              rows={3}
              placeholder="What goal should be locked to the agent while in this state?"
              value={formGoal}
              onChange={(e) => setFormGoal(e.target.value)}
            />
            <small className="form-text text-muted">
              When set, this goal is locked onto agents entering this state and cannot be overwritten by workers.
            </small>
          </div>

          {/* Tool Restrictions section */}
          <div className="workflow-tool-section">
            <button
              type="button"
              className="goal-toggle workflow-tool-master-toggle"
              aria-pressed={formAllToolsAllowed}
              data-testid="allow-all-tools-toggle"
              onClick={() => {
                const nextAllowed = !formAllToolsAllowed;
                setFormAllToolsAllowed(nextAllowed);
                if (nextAllowed) {
                  setFormAllowedTools([]);
                } else if (formAllowedTools.length === 0) {
                  setFormAllowedTools(tools.map((t) => t.id));
                }
              }}
            >
              <div className="workflow-master-toggle-text">
                <span className="goal-toggle-label">Allow all tools (unrestricted)</span>
                <span className="workflow-toggle-subtext">Workers in this state have access to all available tools</span>
              </div>
              <span className="goal-toggle-track" aria-hidden="true">
                <span className="goal-toggle-thumb">
                  <span className="goal-toggle-state">{formAllToolsAllowed ? "ON" : "OFF"}</span>
                </span>
              </span>
            </button>

            {!formAllToolsAllowed ? (
              <div className="workflow-tool-picker">
                <div className="workflow-tool-picker-toolbar">
                  <span className="workflow-tool-picker-title">
                    Allowed Tools ({formAllowedTools.length} of {tools.length})
                  </span>
                  <div className="workflow-tool-picker-actions">
                    <button
                      type="button"
                      className="secondary workflow-tool-action-btn"
                      onClick={() => setFormAllowedTools(tools.map((t) => t.id))}
                    >
                      Select All
                    </button>
                    <button
                      type="button"
                      className="secondary workflow-tool-action-btn"
                      onClick={() => setFormAllowedTools([])}
                    >
                      Deselect All
                    </button>
                  </div>
                </div>

                <div className="workflow-tool-grid">
                  {tools.map((tool) => {
                    const isSelected = formAllowedTools.includes(tool.id);
                    return (
                      <button
                        type="button"
                        key={tool.id}
                        className={`workflow-tool-tile ${isSelected ? "selected" : ""}`}
                        aria-pressed={isSelected}
                        data-testid={`tool-toggle-${tool.id}`}
                        onClick={() => {
                          if (isSelected) {
                            setFormAllowedTools((prev) => prev.filter((id) => id !== tool.id));
                          } else {
                            setFormAllowedTools((prev) => [...prev, tool.id]);
                          }
                        }}
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

          {/* Reminders section */}
          <div className="workflow-reminders-section">
            <div className="workflow-reminders-header">
              <span className="workflow-reminders-title">
                <i className="fa-solid fa-clock" aria-hidden="true" /> Reminders ({formReminders.length})
              </span>
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                onClick={openAddReminder}
              >
                <i className="fa-solid fa-plus" aria-hidden="true" /> Add reminder
              </button>
            </div>

            {formReminders.length === 0 ? (
              <small className="text-muted">
                No reminders configured. Reminders fire while an agent is in this state.
              </small>
            ) : (
              <div className="workflow-reminders-list">
                {formReminders.map((reminder, idx) => (
                  <article className="reminder-list-item" key={reminder.id}>
                    <strong>{reminder.subject || "Reminder"}</strong>
                    <p>{reminder.body}</p>
                    <small className="reminder-recurrence">
                      {reminder.type === "recurring"
                        ? `Recurring every ${formatDuration(reminder.interval_seconds ?? reminder.delay_seconds)}${
                            reminder.delay_seconds > 0 ? ` (starts in ${formatDuration(reminder.delay_seconds)})` : ""
                          }`
                        : `One-shot in ${formatDuration(reminder.delay_seconds)}`}
                    </small>
                    <div className="reminder-lock-actions">
                      <button
                        type="button"
                        className="secondary"
                        onClick={() => openEditReminder(idx)}
                      >
                        Edit
                      </button>
                      <button
                        type="button"
                        className="secondary"
                        onClick={() => removeReminder(idx)}
                      >
                        Delete
                      </button>
                    </div>
                  </article>
                ))}
              </div>
            )}
          </div>

          {/* Exit workflow toggle */}
          <div className="workflow-exit-section">
            <button
              type="button"
              className="goal-toggle workflow-tool-master-toggle"
              aria-pressed={formAllowExit}
              data-testid="allow-exit-toggle"
              onClick={() => setFormAllowExit(!formAllowExit)}
            >
              <div className="workflow-master-toggle-text">
                <span className="goal-toggle-label">Allow Workflow Exit</span>
                <span className="workflow-toggle-subtext">
                  Workers can exit the workflow without transitioning to another state
                </span>
              </div>
              <span className="goal-toggle-track" aria-hidden="true">
                <span className="goal-toggle-thumb">
                  <span className="goal-toggle-state">{formAllowExit ? "ON" : "OFF"}</span>
                </span>
              </span>
            </button>
          </div>

          {/* Transitions section */}
          <div className="workflow-transitions-section">
            <div className="workflow-transitions-header">
              <span className="workflow-transitions-title">
                <i className="fa-solid fa-arrow-right-arrow-left" aria-hidden="true" /> Allowed Transitions ({formTransitions.length})
              </span>
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                data-testid="add-transition-btn"
                onClick={addTransition}
              >
                <i className="fa-solid fa-plus" aria-hidden="true" /> Add transition
              </button>
            </div>

            {formTransitions.length === 0 ? (
              <small className="text-muted">
                No forward transitions configured. Workers can only stay in this state or exit (if exit is allowed).
              </small>
            ) : (
              <div className="workflow-transitions-list">
                {formTransitions.map((tr, idx) => (
                  <div className="workflow-transition-item" key={idx} data-testid={`transition-item-${idx}`}>
                    <div className="workflow-transition-fields">
                      <select
                        className="form-control"
                        aria-label="Target State"
                        data-testid={`transition-target-${idx}`}
                        value={tr.target_state_id}
                        onChange={(e) => updateTransition(idx, "target_state_id", e.target.value)}
                        required
                      >
                        <option value="">Select target state...</option>
                        {states
                          .filter((s) => !editingState || s.id !== editingState.id)
                          .map((s) => (
                            <option key={s.id} value={s.id}>
                              {s.name}
                            </option>
                          ))}
                      </select>
                      <input
                        type="text"
                        className="form-control"
                        aria-label="Transition description"
                        data-testid={`transition-desc-${idx}`}
                        placeholder="Condition / guidance for transition"
                        value={tr.description}
                        onChange={(e) => updateTransition(idx, "description", e.target.value)}
                      />
                    </div>
                    <button
                      type="button"
                      className="btn-icon btn-danger"
                      title="Remove transition"
                      aria-label="Remove transition"
                      data-testid={`remove-transition-${idx}`}
                      onClick={() => removeTransition(idx)}
                    >
                      <i className="fa-solid fa-trash" aria-hidden="true" />
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="dialog-actions workflow-dialog-actions">
            <button type="button" className="btn btn-secondary" onClick={closeForm} disabled={saving}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary" disabled={saving}>
              {saving ? "Saving..." : editingState ? "Save Changes" : "Create State"}
            </button>
          </div>
        </form>
      </DialogShell>

      {/* Reminder Editor Modal Dialog */}
      <DialogShell
        open={isReminderDialogOpen}
        title={editingReminderIndex !== undefined ? "Edit Reminder" : "New Reminder"}
        titleId="workflow-reminder-dialog-title"
        description="Configure a reminder that fires while the agent is in this workflow state."
        onClose={() => setIsReminderDialogOpen(false)}
        closeLabel="Close reminder editor"
        className="message-dialog"
      >
        <form className="reminder-editor-form" noValidate onSubmit={handleSaveReminder}>
          <label>
            Subject
            <input
              value={reminderDraftSubject}
              placeholder="Optional"
              onChange={(e) => setReminderDraftSubject(e.target.value)}
            />
          </label>
          <label>
            Reminder *
            <textarea
              aria-label="Reminder"
              value={reminderDraftBody}
              placeholder="What reminder message to send..."
              rows={4}
              onChange={(e) => {
                setReminderDraftBody(e.target.value);
                setReminderDraftError("");
              }}
              required
            />
          </label>
          <label>
            Delay (seconds)
            <input
              type="number"
              min={1}
              step={1}
              value={reminderDraftDelay}
              onChange={(e) => setReminderDraftDelay(Number(e.target.value))}
            />
          </label>
          <button
            type="button"
            className="goal-toggle"
            aria-pressed={reminderDraftRecurring}
            onClick={() => setReminderDraftRecurring(!reminderDraftRecurring)}
          >
            <span className="goal-toggle-label">Repeat every interval</span>
            <span className="goal-toggle-track" aria-hidden="true">
              <span className="goal-toggle-thumb">
                <span className="goal-toggle-state">{reminderDraftRecurring ? "ON" : "OFF"}</span>
              </span>
            </span>
          </button>
          {reminderDraftRecurring ? (
            <label>
              Repeat interval (seconds)
              <input
                type="number"
                min={1}
                step={1}
                value={reminderDraftInterval}
                onChange={(e) => setReminderDraftInterval(Number(e.target.value))}
              />
            </label>
          ) : null}
          {reminderDraftError ? (
            <p className="reminder-editor-status error" role="alert">
              {reminderDraftError}
            </p>
          ) : null}
          <div className="reminder-lock-actions">
            <button
              type="button"
              className="secondary"
              onClick={() => setIsReminderDialogOpen(false)}
            >
              Cancel
            </button>
            <button type="submit">
              {editingReminderIndex !== undefined ? "Save changes" : "Save reminder"}
            </button>
          </div>
        </form>
      </DialogShell>

      {/* Delete Confirmation Modal Popup */}
      <DialogShell
        open={Boolean(deleteConfirmId)}
        title="Delete Workflow State"
        titleId="delete-state-dialog-title"
        description="Are you sure you want to delete this state? This action cannot be undone."
        onClose={() => setDeleteConfirmId(undefined)}
        closeLabel="Close delete confirmation"
        className="message-dialog delete-confirm-dialog"
      >
        <div className="dialog-actions workflow-dialog-actions">
          <button type="button" className="btn btn-secondary" onClick={() => setDeleteConfirmId(undefined)}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-danger"
            onClick={() => {
              if (deleteConfirmId) {
                void handleDelete(deleteConfirmId);
              }
            }}
          >
            Delete
          </button>
        </div>
      </DialogShell>
    </div>
  );
}

export { WorkflowStatesPage as WorkflowsPage };
export default WorkflowStatesPage;
