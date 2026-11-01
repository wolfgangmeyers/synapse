import { useState, useEffect, useMemo, type ReactElement, type FormEvent } from "react";
import type { Workflow, WorkflowState } from "../domain/workflow-states";
import type { PromptTemplate } from "../domain/prompt-templates";
import type { RouterHttpClient } from "../transport/http-client";
import { renderTemplate } from "../domain/template-engine";
import { DialogShell } from "./DialogShell";

export interface WorkflowLaunchModalProps {
  open: boolean;
  workflow: Workflow;
  agentId: string;
  client: RouterHttpClient;
  templates?: PromptTemplate[] | Record<string, string> | Map<string, string>;
  onClose: () => void;
  onSuccess?: () => void;
}

export function WorkflowLaunchModal({
  open,
  workflow,
  agentId,
  client,
  templates: initialTemplates,
  onClose,
  onSuccess,
}: WorkflowLaunchModalProps): ReactElement | null {
  const [variables, setVariables] = useState<Record<string, string>>({});
  const [entryState, setEntryState] = useState<WorkflowState | null>(null);
  const [fetchedTemplates, setFetchedTemplates] = useState<Record<string, string>>({});
  const [loadingState, setLoadingState] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Normalize or fetch prompt templates
  useEffect(() => {
    if (!open) return;
    if (initialTemplates) {
      if (Array.isArray(initialTemplates)) {
        const map: Record<string, string> = {};
        for (const t of initialTemplates) {
          const key = t.id ?? t.name;
          if (key) map[key] = t.body;
        }
        setFetchedTemplates(map);
      }
      return;
    }

    let mounted = true;
    client
      .promptTemplates?.()
      .then((res) => {
        if (mounted && res?.prompt_templates) {
          const map: Record<string, string> = {};
          for (const t of res.prompt_templates) {
            const key = t.id ?? t.name;
            if (key) map[key] = t.body;
          }
          setFetchedTemplates(map);
        }
      })
      .catch((err) => {
        console.warn("Failed to fetch prompt templates for workflow launch preview", err);
      });

    return () => {
      mounted = false;
    };
  }, [open, initialTemplates, client]);

  const templateMap = useMemo(() => {
    if (initialTemplates && !(Array.isArray(initialTemplates))) {
      return initialTemplates;
    }
    return fetchedTemplates;
  }, [initialTemplates, fetchedTemplates]);

  // Initialize variable defaults when modal opens or workflow changes
  useEffect(() => {
    if (open) {
      const initial: Record<string, string> = {};
      if (workflow.variables) {
        for (const v of workflow.variables) {
          initial[v.name] = v.default_value ?? "";
        }
      }
      setVariables(initial);
      setError(null);
      setSubmitting(false);
    }
  }, [open, workflow]);

  // Fetch entry state if workflow has entry_state_id
  useEffect(() => {
    if (!open || !workflow.entry_state_id) {
      setEntryState(null);
      return;
    }
    let mounted = true;
    setLoadingState(true);
    client
      .getWorkflowState(workflow.entry_state_id)
      .then((res) => {
        if (mounted) {
          setEntryState(res.workflow_state);
        }
      })
      .catch((err) => {
        console.warn("Failed to fetch workflow entry state", err);
      })
      .finally(() => {
        if (mounted) {
          setLoadingState(false);
        }
      });
    return () => {
      mounted = false;
    };
  }, [open, workflow.entry_state_id, client]);

  // Check if any required variable is empty
  const isMissingRequired = useMemo(() => {
    if (!workflow.variables) return false;
    for (const v of workflow.variables) {
      if (v.required) {
        const val = variables[v.name];
        if (!val || val.trim() === "") {
          return true;
        }
      }
    }
    return false;
  }, [workflow.variables, variables]);

  // Live previews rendered with current variable values and prompt templates
  const previewEntryText = useMemo(() => {
    if (!entryState?.entry_text) return "";
    return renderTemplate(entryState.entry_text, variables, templateMap);
  }, [entryState?.entry_text, variables, templateMap]);


  if (!open) {
    return null;
  }

  const handleVariableChange = (name: string, value: string) => {
    setVariables((prev) => ({ ...prev, [name]: value }));
  };

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (isMissingRequired || submitting) return;

    setSubmitting(true);
    setError(null);
    try {
      await client.setManagedAgentWorkflow(agentId, workflow.id, variables);
      onSuccess?.();
      onClose();
    } catch (err: unknown) {
      console.error("Failed to launch workflow", err);
      setError(err instanceof Error ? err.message : "Failed to launch workflow");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <DialogShell
      open={open}
      title={`Launch Workflow: ${workflow.name}`}
      titleId="workflow-launch-dialog-title"
      description={workflow.description || "Configure workflow variables before launching."}
      onClose={onClose}
      closeLabel="Close workflow launch modal"
      className="message-dialog workflow-launch-dialog"
    >
      <form onSubmit={handleSubmit} style={{ display: "flex", flexDirection: "column", gap: "1rem" }}>
        {error ? (
          <div
            style={{
              padding: "10px 14px",
              background: "rgba(239, 68, 68, 0.15)",
              border: "1px solid rgba(239, 68, 68, 0.3)",
              borderRadius: "8px",
              color: "#f87171",
              fontSize: "0.85rem",
            }}
          >
            {error}
          </div>
        ) : null}

        {workflow.variables && workflow.variables.length > 0 ? (
          <div style={{ display: "flex", flexDirection: "column", gap: "0.85rem" }}>
            <div style={{ fontSize: "0.9rem", fontWeight: 600, color: "var(--text, #eee)" }}>
              Variables
            </div>
            {workflow.variables.map((variable) => (
              <div key={variable.name} className="form-group" style={{ display: "flex", flexDirection: "column", gap: "0.3rem" }}>
                <label
                  htmlFor={`var-${variable.name}`}
                  style={{ display: "flex", alignItems: "center", gap: "4px", fontSize: "0.85rem", fontWeight: 600 }}
                >
                  <span>{variable.label || variable.name}</span>
                  {variable.required ? (
                    <span style={{ color: "#f87171", fontWeight: "bold" }}>*</span>
                  ) : null}
                  <code style={{ fontSize: "0.75rem", color: "var(--muted, #888)", marginLeft: "4px" }}>
                    ${`{${variable.name}}`}
                  </code>
                </label>
                {variable.description ? (
                  <small style={{ color: "var(--text-secondary, #9ca3af)", fontSize: "0.78rem" }}>
                    {variable.description}
                  </small>
                ) : null}
                {variable.type === "textarea" ? (
                  <textarea
                    id={`var-${variable.name}`}
                    className="form-control"
                    rows={3}
                    value={variables[variable.name] ?? ""}
                    onChange={(e) => handleVariableChange(variable.name, e.target.value)}
                    placeholder={variable.default_value || `Enter ${variable.label || variable.name}`}
                    required={variable.required}
                  />
                ) : (
                  <input
                    id={`var-${variable.name}`}
                    type="text"
                    className="form-control"
                    value={variables[variable.name] ?? ""}
                    onChange={(e) => handleVariableChange(variable.name, e.target.value)}
                    placeholder={variable.default_value || `Enter ${variable.label || variable.name}`}
                    required={variable.required}
                  />
                )}
              </div>
            ))}
          </div>
        ) : null}

        {/* Live preview */}
        {entryState ? (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "0.6rem",
              background: "var(--canvas, #10141f)",
              padding: "12px",
              borderRadius: "8px",
              border: "1px solid var(--line, rgba(255, 255, 255, 0.1))",
            }}
          >
            <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
              <span style={{ fontSize: "0.8rem", fontWeight: 700, textTransform: "uppercase", color: "var(--muted, #888)", letterSpacing: "0.04em" }}>
                Entry State Preview ({entryState.name})
              </span>
              {loadingState ? <span style={{ fontSize: "0.75rem", color: "var(--muted, #888)" }}>Loading...</span> : null}
            </div>

            {previewEntryText ? (
              <div>
                <span style={{ fontSize: "0.75rem", fontWeight: 600, color: "var(--text-secondary, #aaa)" }}>
                  Entry Instructions:
                </span>
                <pre
                  style={{
                    margin: "4px 0 0 0",
                    padding: "8px",
                    background: "var(--surface-raised, #1a202c)",
                    borderRadius: "6px",
                    fontSize: "0.8rem",
                    whiteSpace: "pre-wrap",
                    wordBreak: "break-word",
                    maxHeight: "120px",
                    overflowY: "auto",
                  }}
                >
                  {previewEntryText}
                </pre>
              </div>
            ) : null}

            {!previewEntryText ? (
              <span style={{ fontSize: "0.8rem", color: "var(--muted, #888)", fontStyle: "italic" }}>
                Entry state has no instructions text.
              </span>
            ) : null}
          </div>
        ) : null}

        <div className="dialog-actions workflow-dialog-actions" style={{ justifyContent: "flex-end", gap: "0.5rem", marginTop: "0.5rem" }}>
          <button type="button" className="btn btn-secondary" onClick={onClose} disabled={submitting}>
            Cancel
          </button>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={isMissingRequired || submitting}
          >
            {submitting ? "Launching…" : "Launch"}
          </button>
        </div>
      </form>
    </DialogShell>
  );
}
