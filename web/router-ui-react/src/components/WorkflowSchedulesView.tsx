import { useState, useEffect, useMemo, type ReactElement, type FormEvent } from "react";
import type { RouterHttpClient } from "../transport/http-client";
import type { WorkflowSchedule } from "../protocol/types";
import type { Workflow } from "../domain/workflow-states";
import { WorkflowSelect } from "./WorkflowSelect";
import { ScheduleCronPicker, describeCron, intervalToCron } from "./ScheduleCronPicker";
import { ManagedDirectoryPicker } from "./ManagedDirectoryPicker";
import { loadManagedRecentFolders, recordManagedRecentFolder, saveManagedRecentFolders } from "../persistence/managed-folder-history";

export interface WorkflowSchedulesViewProps {
  client: RouterHttpClient;
  recentFolders?: readonly string[] | undefined;
  onListDirectories?: ((path: string) => Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>) | undefined;
  onWorkflowRun?: ((workflowId: string) => void) | undefined;
}

export function WorkflowSchedulesView({ client, recentFolders: propRecentFolders, onListDirectories, onWorkflowRun: _onWorkflowRun }: WorkflowSchedulesViewProps): ReactElement {
  const [viewMode, setViewMode] = useState<"list" | "form">("list");
  const [schedules, setSchedules] = useState<WorkflowSchedule[]>([]);
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  // Form state
  const [editingId, setEditingId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [workflowId, setWorkflowId] = useState("");
  const [cronExpression, setCronExpression] = useState("0 * * * *");
  const [variables, setVariables] = useState<Record<string, string>>({});
  const [targetCwd, setTargetCwd] = useState("");
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const fallbackRecentFolders = useMemo(() => loadManagedRecentFolders(), []);
  const activeRecentFolders = propRecentFolders ?? fallbackRecentFolders;

  const handleListDirectories = onListDirectories ?? ((path: string) => client.managedDirectories(path));

  const loadData = async (): Promise<void> => {
    setLoading(true);
    setError(null);
    try {
      const [schedList, wfList] = await Promise.all([
        client.listWorkflowSchedules(),
        Promise.resolve().then(() => client.listWorkflows()).catch(() => []),
      ]);
      setSchedules(Array.isArray(schedList) ? schedList : []);
      setWorkflows(Array.isArray(wfList) ? wfList : []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load workflow schedules");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadData();
  }, [client]);

  const resetForm = () => {
    setEditingId(null);
    setName("");
    setWorkflowId("");
    setCronExpression("0 * * * *");
    setVariables({});
    setTargetCwd("");
    setFormError(null);
  };

  const selectedWorkflow = workflows.find((w) => w.id === workflowId);

  const handleStartAdd = () => {
    resetForm();
    setViewMode("form");
  };

  const handleStartEdit = (schedule: WorkflowSchedule) => {
    setEditingId(schedule.id);
    setName(schedule.name);
    setWorkflowId(schedule.workflow_id);
    // Seamlessly feed either cron_expression or legacy interval_ms into ScheduleCronPicker
    if (schedule.cron_expression && schedule.cron_expression.trim()) {
      setCronExpression(schedule.cron_expression.trim());
    } else if (schedule.interval_ms) {
      setCronExpression(intervalToCron(schedule.interval_ms));
    } else {
      setCronExpression("0 * * * *");
    }
    setVariables(schedule.variables ?? {});
    setTargetCwd(schedule.target_cwd ?? "");
    setFormError(null);
    setViewMode("form");
  };

  const handleWorkflowChange = (newWorkflowId: string) => {
    setWorkflowId(newWorkflowId);
    const wf = workflows.find((w) => w.id === newWorkflowId);
    const initialVars: Record<string, string> = {};
    if (wf?.variables) {
      for (const v of wf.variables) {
        initialVars[v.name] = variables[v.name] ?? v.default_value ?? "";
      }
    }
    setVariables(initialVars);
  };

  const handleToggleEnabled = async (schedule: WorkflowSchedule): Promise<void> => {
    try {
      await client.updateWorkflowSchedule(schedule.id, { enabled: !schedule.enabled });
      await loadData();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to toggle schedule");
    }
  };

  const handleDelete = async (schedule: WorkflowSchedule): Promise<void> => {
    if (deletingId) return;
    setDeletingId(schedule.id);
    setError(null);
    try {
      await client.deleteWorkflowSchedule(schedule.id);
      setStatusMessage(`Deleted schedule "${schedule.name}".`);
      await loadData();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete schedule");
    } finally {
      setDeletingId(null);
    }
  };

  const handleSaveForm = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    setFormError(null);

    const trimmedName = name.trim();
    if (!trimmedName) {
      setFormError("Schedule name is required.");
      return;
    }

    if (!workflowId) {
      setFormError("A target workflow must be selected.");
      return;
    }

    if (!cronExpression.trim()) {
      setFormError("Cron expression is required.");
      return;
    }

    // Strict validation of required variables
    if (selectedWorkflow?.variables) {
      for (const v of selectedWorkflow.variables) {
        if (v.required) {
          const val = variables[v.name];
          if (!val || !val.trim()) {
            setFormError(`Required variable "${v.label || v.name}" must not be empty.`);
            return;
          }
        }
      }
    }

    setSaving(true);
    try {
      const payload: Partial<WorkflowSchedule> = {
        name: trimmedName,
        workflow_id: workflowId,
        schedule_type: "cron",
        cron_expression: cronExpression.trim(),
        interval_ms: null,
        variables,
        ...(targetCwd.trim() ? { target_cwd: targetCwd.trim() } : {}),
      };

      if (targetCwd.trim()) {
        saveManagedRecentFolders(undefined, recordManagedRecentFolder(activeRecentFolders, targetCwd.trim()));
      }

      if (editingId) {
        await client.updateWorkflowSchedule(editingId, payload);
        setStatusMessage(`Updated schedule "${trimmedName}".`);
      } else {
        await client.createWorkflowSchedule({
          ...payload,
          enabled: true,
        });
        setStatusMessage(`Created schedule "${trimmedName}".`);
      }

      setViewMode("list");
      resetForm();
      await loadData();
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "Failed to save schedule.");
    } finally {
      setSaving(false);
    }
  };

  const formatNextRun = (timestampStr: string): string => {
    if (!timestampStr) return "N/A";
    const date = new Date(timestampStr);
    if (Number.isNaN(date.getTime())) return timestampStr;
    return date.toLocaleString();
  };

  const renderStatusBadge = (status?: string | null) => {
    if (!status) return <span className="mcp-server-badge default">Idle</span>;
    if (status === "success") return <span className="mcp-server-badge connected">Success</span>;
    if (status === "failed") return <span className="mcp-server-badge error">Failed</span>;
    if (status === "running") return <span className="mcp-server-badge connecting">Running</span>;
    return <span className="mcp-server-badge default">{status}</span>;
  };

  return (
    <div className="workflow-schedules-view" style={{ width: "100%", maxWidth: 960, margin: "0 auto" }}>
      {error ? (
        <div className="alert alert-danger" role="alert" style={{ marginBottom: "1rem" }}>
          {error}
        </div>
      ) : null}

      {statusMessage ? (
        <div className="alert alert-success" role="status" style={{ marginBottom: "1rem" }}>
          {statusMessage}
        </div>
      ) : null}

      {viewMode === "form" ? (
        <section className="mcp-add-section" style={{ background: "var(--surface)", border: "1px solid var(--line)", borderRadius: "12px", padding: "20px" }}>
          <div className="mcp-section-header" style={{ display: "flex", alignItems: "center", gap: "12px", marginBottom: "16px" }}>
            <button
              type="button"
              className="icon-btn-round"
              title="Back to schedules list"
              aria-label="Back to schedules list"
              onClick={() => {
                setViewMode("list");
                resetForm();
              }}
            >
              <i className="fa-solid fa-arrow-left" aria-hidden="true" />
            </button>
            <h3 style={{ margin: 0 }}>{editingId ? "Edit Schedule" : "Add Schedule"}</h3>
          </div>

          {formError ? (
            <div className="alert alert-danger" role="alert" style={{ marginBottom: "1rem" }}>
              {formError}
            </div>
          ) : null}

          <form onSubmit={(e) => { void handleSaveForm(e); }} className="mcp-form">
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="schedule-name" style={{ display: "block", marginBottom: "4px", fontWeight: 600 }}>
                Schedule Name *
              </label>
              <input
                id="schedule-name"
                type="text"
                className="form-control"
                placeholder="e.g. Daily Health Sweep"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>

            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="schedule-workflow" style={{ display: "block", marginBottom: "4px", fontWeight: 600 }}>
                Target Workflow *
              </label>
              <WorkflowSelect
                workflows={workflows}
                value={workflowId}
                onChange={handleWorkflowChange}
                placeholder="Select a workflow..."
              />
            </div>

            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label style={{ display: "block", marginBottom: "4px", fontWeight: 600 }}>Schedule *</label>
              <ScheduleCronPicker value={cronExpression} onChange={setCronExpression} />
            </div>

            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <ManagedDirectoryPicker
                label="Working Directory (optional)"
                required={false}
                value={targetCwd}
                recentFolders={activeRecentFolders}
                placeholder="e.g. /home/user/project or repo root"
                onChange={setTargetCwd}
                list={handleListDirectories}
              />
            </div>

            {/* Workflow Variables Section */}
            {selectedWorkflow && selectedWorkflow.variables && selectedWorkflow.variables.length > 0 ? (
              <div className="form-group" style={{ marginTop: "1rem", marginBottom: "1rem" }}>
                <label style={{ fontWeight: 600, color: "var(--text-bright, #fff)", display: "block", marginBottom: "4px" }}>
                  Workflow Variables
                </label>
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "10px",
                    marginTop: "6px",
                    padding: "12px",
                    background: "rgba(13, 20, 36, 0.45)",
                    borderRadius: "8px",
                    border: "1px solid rgba(255, 255, 255, 0.08)",
                  }}
                >
                  {selectedWorkflow.variables.map((variable) => (
                    <div key={variable.name} className="variable-input-field">
                      <label
                        htmlFor={`var-${variable.name}`}
                        style={{
                          fontSize: "0.85rem",
                          display: "flex",
                          justifyContent: "space-between",
                          marginBottom: "4px",
                        }}
                      >
                        <span>
                          {variable.label || variable.name}{" "}
                          {variable.required ? <span style={{ color: "var(--danger, #ef4444)" }}>*</span> : null}
                        </span>
                        <code style={{ fontSize: "0.75rem", color: "var(--muted)" }}>{variable.name}</code>
                      </label>
                      {variable.description ? (
                        <div style={{ fontSize: "0.75rem", color: "var(--muted)", marginBottom: "4px" }}>
                          {variable.description}
                        </div>
                      ) : null}
                      {variable.type === "textarea" ? (
                        <textarea
                          id={`var-${variable.name}`}
                          className="form-control"
                          rows={3}
                          value={variables[variable.name] ?? ""}
                          onChange={(e) =>
                            setVariables((prev) => ({
                              ...prev,
                              [variable.name]: e.target.value,
                            }))
                          }
                          placeholder={variable.default_value ? `Default: ${variable.default_value}` : ""}
                          required={variable.required}
                        />
                      ) : (
                        <input
                          id={`var-${variable.name}`}
                          type="text"
                          className="form-control"
                          value={variables[variable.name] ?? ""}
                          onChange={(e) =>
                            setVariables((prev) => ({
                              ...prev,
                              [variable.name]: e.target.value,
                            }))
                          }
                          placeholder={variable.default_value ? `Default: ${variable.default_value}` : ""}
                          required={variable.required}
                        />
                      )}
                    </div>
                  ))}
                </div>
              </div>
            ) : null}

            <div className="form-actions" style={{ display: "flex", gap: "8px", marginTop: "1.5rem" }}>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => {
                  setViewMode("list");
                  resetForm();
                }}
                disabled={saving}
              >
                Cancel
              </button>
              <button type="submit" className="btn btn-primary" disabled={saving}>
                {saving ? (
                  <>
                    <i className="fa-solid fa-arrows-rotate fa-spin" aria-hidden="true" /> Saving...
                  </>
                ) : (
                  <>
                    <i className="fa-solid fa-check" aria-hidden="true" /> {editingId ? "Save Changes" : "Create Schedule"}
                  </>
                )}
              </button>
            </div>
          </form>
        </section>
      ) : (
        /* Configured Schedules List */
        <section className="mcp-list-section" style={{ background: "var(--surface)", border: "1px solid var(--line)", borderRadius: "12px", padding: "20px" }}>
          <div className="mcp-list-header" style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "16px" }}>
            <h3 style={{ margin: 0 }}>Configured Schedules ({schedules.length})</h3>
            <div className="mcp-header-actions" style={{ display: "flex", gap: "8px", alignItems: "center" }}>
              <button
                type="button"
                className="icon-btn-round"
                title="Refresh schedules"
                aria-label="Refresh schedules"
                onClick={() => void loadData()}
                disabled={loading}
              >
                <i className={`fa-solid fa-arrows-rotate ${loading ? "fa-spin" : ""}`} aria-hidden="true" />
              </button>
              <button
                type="button"
                className="btn btn-primary mcp-add-btn"
                onClick={handleStartAdd}
              >
                + Add Schedule
              </button>
            </div>
          </div>

          {schedules.length === 0 ? (
            <div className="mcp-empty-state" style={{ padding: "32px 16px", textAlign: "center", color: "var(--muted)" }}>
              {loading ? "Loading schedules..." : "No workflow schedules configured yet. Click \"+ Add Schedule\" above to create one."}
            </div>
          ) : (
            <div className="mcp-server-cards" style={{ display: "flex", flexDirection: "column", gap: "10px" }}>
              {schedules.map((schedule) => {
                const isDeleting = deletingId === schedule.id;
                const targetWf = workflows.find((w) => w.id === schedule.workflow_id);

                return (
                  <div key={schedule.id} className="mcp-server-card">
                    <div className="mcp-server-main">
                      <div className="mcp-server-title-line">
                        <strong className="mcp-server-name">{schedule.name}</strong>
                        {renderStatusBadge(schedule.last_run_status)}
                        <span
                          className="badge"
                          style={{
                            fontSize: "0.75rem",
                            background: schedule.enabled ? "rgba(52, 211, 153, 0.2)" : "rgba(148, 163, 184, 0.2)",
                            color: schedule.enabled ? "#34d399" : "#94a3b8",
                            borderRadius: "4px",
                            padding: "2px 6px",
                          }}
                        >
                          {schedule.enabled ? "Enabled" : "Disabled"}
                        </span>
                      </div>

                      <div style={{ display: "flex", gap: "12px", alignItems: "center", fontSize: "0.85rem", color: "var(--muted)", margin: "4px 0", flexWrap: "wrap" }}>
                        <span>
                          Target: <strong style={{ color: "#93c5fd" }}>{targetWf?.name || schedule.workflow_id}</strong>
                        </span>
                        <span>•</span>
                        <span>
                          Schedule: <strong style={{ color: "#c084fc" }}>{describeCron(schedule.cron_expression ?? (schedule.interval_ms ? intervalToCron(schedule.interval_ms) : ""))}</strong>
                        </span>
                        {schedule.cron_expression ? (
                          <>
                            <span>•</span>
                            <code style={{ fontSize: "0.78rem", color: "#a5b4fc" }}>{schedule.cron_expression}</code>
                          </>
                        ) : null}
                      </div>

                      <div className="mcp-server-meta">
                        <span className="mcp-meta-item">
                          <i className="fa-solid fa-clock" aria-hidden="true" />
                          <span>Next run: {formatNextRun(schedule.next_run_at)}</span>
                        </span>
                        {schedule.last_run_at ? (
                          <span className="mcp-meta-item">
                            <i className="fa-solid fa-history" aria-hidden="true" />
                            <span>Last run: {formatNextRun(schedule.last_run_at)}</span>
                          </span>
                        ) : null}
                      </div>

                      {schedule.last_run_error ? (
                        <div className="mcp-server-error-text" title={schedule.last_run_error}>
                          <i className="fa-solid fa-circle-exclamation" aria-hidden="true" />
                          <span>{schedule.last_run_error}</span>
                        </div>
                      ) : null}
                    </div>

                    <div className="mcp-server-actions">
                      <button
                        type="button"
                        className="icon-btn-round"
                        title={schedule.enabled ? "Disable schedule" : "Enable schedule"}
                        aria-label={schedule.enabled ? "Disable schedule" : "Enable schedule"}
                        onClick={() => void handleToggleEnabled(schedule)}
                      >
                        <i className={`fa-solid ${schedule.enabled ? "fa-toggle-on" : "fa-toggle-off"}`} aria-hidden="true" />
                      </button>

                      <button
                        type="button"
                        className="icon-btn-round"
                        title="Edit schedule"
                        aria-label={`Edit ${schedule.name}`}
                        onClick={() => handleStartEdit(schedule)}
                      >
                        <i className="fa-solid fa-pencil" aria-hidden="true" />
                      </button>

                      <button
                        type="button"
                        className="icon-btn-round danger"
                        title="Delete schedule"
                        aria-label={`Delete ${schedule.name}`}
                        onClick={() => void handleDelete(schedule)}
                        disabled={isDeleting}
                      >
                        <i className={`fa-solid ${isDeleting ? "fa-arrows-rotate fa-spin" : "fa-trash"}`} aria-hidden="true" />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </section>
      )}
    </div>
  );
}
