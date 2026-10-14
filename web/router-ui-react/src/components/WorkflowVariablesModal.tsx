import { useState, useEffect, type ReactElement, type FormEvent } from "react";
import type { Workflow, WorkflowVariableDefinition, WorkflowVariableType } from "../domain/workflow-states";
import { DialogShell } from "./DialogShell";

export interface WorkflowVariablesModalProps {
  isOpen: boolean;
  onClose: () => void;
  workflow: Workflow;
  onSave: (variables: WorkflowVariableDefinition[]) => Promise<void>;
}

const VARIABLE_NAME_REGEX = /^[a-zA-Z0-9_-]+$/;

export function WorkflowVariablesModal({
  isOpen,
  onClose,
  workflow,
  onSave,
}: WorkflowVariablesModalProps): ReactElement | null {
  const [variables, setVariables] = useState<WorkflowVariableDefinition[]>(
    workflow.variables ? workflow.variables.map((v) => ({ ...v })) : []
  );

  // Screen view mode: "list" (overview of variables) or "form" (dedicated add/edit screen)
  const [viewMode, setViewMode] = useState<"list" | "form">("list");

  // Sync state whenever workflow or modal opening changes
  useEffect(() => {
    if (isOpen) {
      setVariables(workflow.variables ? workflow.variables.map((v) => ({ ...v })) : []);
      resetForm();
      setViewMode("list");
      setError(null);
    }
  }, [isOpen, workflow]);

  // Form state
  const [editingIndex, setEditingIndex] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [type, setType] = useState<WorkflowVariableType>("text");
  const [required, setRequired] = useState(false);
  const [defaultValue, setDefaultValue] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  // Modal actions state
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!isOpen) {
    return null;
  }

  const resetForm = () => {
    setEditingIndex(null);
    setName("");
    setDescription("");
    setType("text");
    setRequired(false);
    setDefaultValue("");
    setFormError(null);
  };

  const startCreate = () => {
    resetForm();
    setViewMode("form");
  };

  const startEdit = (index: number) => {
    const v = variables[index];
    if (!v) return;
    setEditingIndex(index);
    setName(v.name);
    setDescription(v.description ?? "");
    setType(v.type);
    setRequired(Boolean(v.required));
    setDefaultValue(v.default_value ?? "");
    setFormError(null);
    setViewMode("form");
  };

  const cancelForm = () => {
    resetForm();
    setViewMode("list");
  };

  const handleDelete = (index: number) => {
    if (editingIndex === index) {
      resetForm();
    } else if (editingIndex !== null && editingIndex > index) {
      setEditingIndex(editingIndex - 1);
    }
    setVariables((prev) => prev.filter((_, i) => i !== index));
  };

  const handleAddOrUpdate = (e: FormEvent) => {
    e.preventDefault();
    setFormError(null);

    const trimmedName = name.trim();

    if (!trimmedName) {
      setFormError("Variable name is required.");
      return;
    }

    if (!VARIABLE_NAME_REGEX.test(trimmedName)) {
      setFormError("Variable name may only contain letters, numbers, underscores, and hyphens.");
      return;
    }

    // Check uniqueness (ignoring the variable currently being edited)
    const duplicate = variables.some(
      (v, idx) => idx !== editingIndex && v.name.toLowerCase() === trimmedName.toLowerCase()
    );
    if (duplicate) {
      setFormError(`A variable named "${trimmedName}" already exists.`);
      return;
    }

    const definition: WorkflowVariableDefinition = {
      name: trimmedName,
      label: trimmedName,
      type,
      ...(description.trim() ? { description: description.trim() } : {}),
      ...(required ? { required: true } : { required: false }),
      ...(defaultValue ? { default_value: defaultValue } : {}),
    };

    if (editingIndex !== null) {
      setVariables((prev) => prev.map((v, i) => (i === editingIndex ? definition : v)));
    } else {
      setVariables((prev) => [...prev, definition]);
    }

    resetForm();
    setViewMode("list");
  };

  const handleSaveModal = async () => {
    try {
      setSaving(true);
      setError(null);
      await onSave(variables);
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save workflow variables.");
    } finally {
      setSaving(false);
    }
  };

  if (viewMode === "form") {
    return (
      <DialogShell
        open={isOpen}
        title={editingIndex !== null ? `Edit Variable: ${variables[editingIndex]?.name}` : "Add Variable"}
        titleId="workflow-variable-form-dialog-title"
        description={
          editingIndex !== null
            ? "Update configuration for this workflow variable."
            : "Configure a new input variable for this workflow."
        }
        onClose={cancelForm}
        closeLabel="Close variable form"
        className="message-dialog workflow-dialog"
      >
        <form
          onSubmit={handleAddOrUpdate}
          className="workflow-variables-modal-content"
          style={{ display: "flex", flexDirection: "column", gap: "1rem" }}
        >
          {formError && (
            <div
              className="alert alert-danger"
              style={{
                color: "#f87171",
                backgroundColor: "rgba(127, 29, 29, 0.4)",
                border: "1px solid #991b1b",
                borderRadius: "6px",
                padding: "8px 12px",
                fontSize: "13px",
              }}
            >
              {formError}
            </div>
          )}

          <div className="form-group">
            <label htmlFor="var-name" style={{ display: "block", marginBottom: "0.25rem", fontSize: "0.85rem", fontWeight: 600 }}>
              Variable Name *
            </label>
            <input
              id="var-name"
              type="text"
              className="form-control"
              placeholder="e.g. repo_url"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>

          <div style={{ display: "grid", gridTemplateColumns: "1fr auto", gap: "0.75rem", alignItems: "center" }}>
            <div className="form-group">
              <label htmlFor="var-type" style={{ display: "block", marginBottom: "0.25rem", fontSize: "0.85rem", fontWeight: 600 }}>
                Type *
              </label>
              <select
                id="var-type"
                className="form-control"
                value={type}
                onChange={(e) => setType(e.target.value as WorkflowVariableType)}
              >
                <option value="text">Single-line Text (input)</option>
                <option value="textarea">Multi-line Text (textarea)</option>
              </select>
            </div>
            <div style={{ display: "flex", alignItems: "center", marginTop: "1.25rem" }}>
              <label htmlFor="var-required" style={{ display: "inline-flex", alignItems: "center", gap: "0.5rem", cursor: "pointer", fontSize: "0.85rem", fontWeight: 600 }}>
                <input
                  id="var-required"
                  type="checkbox"
                  checked={required}
                  onChange={(e) => setRequired(e.target.checked)}
                />
                Required
              </label>
            </div>
          </div>

          <div className="form-group">
            <label htmlFor="var-desc" style={{ display: "block", marginBottom: "0.25rem", fontSize: "0.85rem", fontWeight: 600 }}>
              Description / Hint (optional)
            </label>
            <input
              id="var-desc"
              type="text"
              className="form-control"
              placeholder="e.g. Git repository clone URL"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>

          <div className="form-group">
            <label htmlFor="var-default" style={{ display: "block", marginBottom: "0.25rem", fontSize: "0.85rem", fontWeight: 600 }}>
              Default Value (optional)
            </label>
            {type === "textarea" ? (
              <textarea
                id="var-default"
                className="form-control"
                rows={3}
                placeholder="Default value if not provided"
                value={defaultValue}
                onChange={(e) => setDefaultValue(e.target.value)}
              />
            ) : (
              <input
                id="var-default"
                type="text"
                className="form-control"
                placeholder="Default value if not provided"
                value={defaultValue}
                onChange={(e) => setDefaultValue(e.target.value)}
              />
            )}
          </div>

          <div className="dialog-actions workflow-dialog-actions" style={{ marginTop: "0.5rem" }}>
            <button type="button" className="btn btn-secondary" onClick={cancelForm}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary">
              {editingIndex !== null ? "Save Variable" : "Add Variable"}
            </button>
          </div>
        </form>
      </DialogShell>
    );
  }

  return (
    <DialogShell
      open={isOpen}
      title={`Workflow Variables: ${workflow.name}`}
      titleId="workflow-variables-dialog-title"
      description="Define input variables for this workflow. Variables can be referenced in state prompts as ${VAR} or {{VAR}}."
      onClose={onClose}
      closeLabel="Close workflow variables dialog"
      className="message-dialog workflow-dialog"
    >
      <div className="workflow-variables-modal-content" style={{ display: "flex", flexDirection: "column", gap: "1.25rem" }}>
        {error && (
          <div
            className="alert alert-danger"
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
        )}

        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
          <h4 style={{ margin: 0, fontSize: "0.95rem", fontWeight: 600 }}>
            Configured Variables ({variables.length})
          </h4>
          <button type="button" className="btn btn-primary btn-sm" onClick={startCreate}>
            + Add Variable
          </button>
        </div>

        {variables.length === 0 ? (
          <p style={{ color: "var(--text-secondary, #9ca3af)", fontSize: "0.85rem", fontStyle: "italic", margin: "0.25rem 0" }}>
            No variables configured yet. Click "+ Add Variable" to create one.
          </p>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: "0.5rem", maxHeight: "280px", overflowY: "auto" }}>
            {variables.map((variable, idx) => (
              <div
                key={variable.name}
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  padding: "8px 12px",
                  background: "var(--surface-raised, rgba(255, 255, 255, 0.04))",
                  border: "1px solid var(--line, rgba(255, 255, 255, 0.1))",
                  borderRadius: "6px",
                  gap: "8px",
                }}
              >
                <div style={{ display: "flex", flexDirection: "column", gap: "2px", minWidth: 0, flex: 1 }}>
                  <div style={{ display: "flex", alignItems: "center", gap: "8px", flexWrap: "wrap" }}>
                    <code style={{ fontWeight: 600, color: "#60a5fa" }}>${`{${variable.name}}`}</code>
                    <span
                      style={{
                        fontSize: "0.75rem",
                        padding: "1px 6px",
                        borderRadius: "4px",
                        background: variable.type === "textarea" ? "rgba(168, 85, 247, 0.15)" : "rgba(59, 130, 246, 0.15)",
                        color: variable.type === "textarea" ? "#c084fc" : "#93c5fd",
                        border: `1px solid ${variable.type === "textarea" ? "rgba(168, 85, 247, 0.3)" : "rgba(59, 130, 246, 0.3)"}`,
                      }}
                    >
                      {variable.type}
                    </span>
                    <span
                      style={{
                        fontSize: "0.75rem",
                        padding: "1px 6px",
                        borderRadius: "4px",
                        background: variable.required ? "rgba(239, 68, 68, 0.15)" : "rgba(107, 114, 128, 0.2)",
                        color: variable.required ? "#f87171" : "#9ca3af",
                        border: `1px solid ${variable.required ? "rgba(239, 68, 68, 0.3)" : "rgba(107, 114, 128, 0.3)"}`,
                      }}
                    >
                      {variable.required ? "Required" : "Optional"}
                    </span>
                  </div>
                  {variable.description && (
                    <span style={{ fontSize: "0.8rem", color: "var(--text-secondary, #9ca3af)" }}>
                      {variable.description}
                    </span>
                  )}
                  {variable.default_value !== undefined && variable.default_value !== "" && (
                    <span style={{ fontSize: "0.78rem", color: "var(--text-secondary, #9ca3af)" }}>
                      Default: <code>{variable.default_value}</code>
                    </span>
                  )}
                </div>
                <div style={{ display: "flex", gap: "6px" }}>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    onClick={() => startEdit(idx)}
                    title="Edit variable"
                    style={{ padding: "4px 8px" }}
                  >
                    <i className="fa-solid fa-pen" aria-hidden="true" />
                  </button>
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    onClick={() => handleDelete(idx)}
                    title="Delete variable"
                    style={{ padding: "4px 8px", color: "#f87171" }}
                  >
                    <i className="fa-solid fa-trash" aria-hidden="true" />
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}

        <div className="dialog-actions workflow-dialog-actions" style={{ marginTop: "0.5rem" }}>
          <button type="button" className="btn btn-secondary" onClick={onClose} disabled={saving}>
            Cancel
          </button>
          <button type="button" className="btn btn-primary" onClick={() => void handleSaveModal()} disabled={saving}>
            {saving ? "Saving..." : "Save Variables"}
          </button>
        </div>
      </div>
    </DialogShell>
  );
}
