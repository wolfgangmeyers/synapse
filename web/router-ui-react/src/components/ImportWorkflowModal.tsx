import { useState, useRef, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import type { WorkflowExportBundle } from "../domain/workflow-states";

export interface ImportWorkflowModalProps {
  open: boolean;
  onClose: () => void;
  onImport: (bundle: WorkflowExportBundle) => Promise<void>;
}

export function ImportWorkflowModal({
  open,
  onClose,
  onImport,
}: ImportWorkflowModalProps): ReactElement | null {
  const [bundle, setBundle] = useState<WorkflowExportBundle | null>(null);
  const [fileName, setFileName] = useState<string>("");
  const [parseError, setParseError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  if (!open) return null;

  const processFileContent = (content: string, name: string) => {
    try {
      setParseError(null);
      const parsed = JSON.parse(content) as unknown;
      if (!parsed || typeof parsed !== "object") {
        throw new Error("File does not contain a valid JSON object");
      }
      const b = parsed as WorkflowExportBundle;
      if (!b.schema_version) {
        throw new Error("Missing schema_version in workflow bundle");
      }
      if (!b.workflow || typeof b.workflow.name !== "string" || !b.workflow.name.trim()) {
        throw new Error("Bundle must contain a valid workflow name");
      }
      if (!Array.isArray(b.states) || b.states.length === 0) {
        throw new Error("Bundle must contain at least one state in 'states'");
      }
      setBundle(b);
      setFileName(name);
    } catch (err: unknown) {
      setBundle(null);
      setFileName("");
      setParseError(err instanceof Error ? err.message : "Failed to parse JSON bundle");
    }
  };

  const handleFileChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (event) => {
      const content = event.target?.result as string;
      processFileContent(content, file.name);
    };
    reader.readAsText(file);
  };

  const handleDrop = (e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    setIsDragging(false);
    const file = e.dataTransfer.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = (event) => {
      const content = event.target?.result as string;
      processFileContent(content, file.name);
    };
    reader.readAsText(file);
  };

  const handleImportSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!bundle) return;
    try {
      setImporting(true);
      setParseError(null);
      await onImport(bundle);
      onClose();
    } catch (err: unknown) {
      setParseError(err instanceof Error ? err.message : "Failed to import workflow");
    } finally {
      setImporting(false);
    }
  };

  return (
    <DialogShell
      open={open}
      title="Import Workflow"
      titleId="import-workflow-dialog-title"
      description="Upload a workflow JSON file to import it into your workspace."
      onClose={onClose}
      closeLabel="Close import workflow dialog"
      className="message-dialog"
    >
      <form onSubmit={(e) => void handleImportSubmit(e)}>
        {parseError && <div className="alert alert-danger" role="alert">{parseError}</div>}

        <input
          type="file"
          ref={fileInputRef}
          accept=".json,application/json"
          style={{ display: "none" }}
          onChange={handleFileChange}
        />

        {!bundle ? (
          <div
            className={`file-dropzone ${isDragging ? "active" : ""}`}
            onDragOver={(e) => {
              e.preventDefault();
              setIsDragging(true);
            }}
            onDragLeave={() => setIsDragging(false)}
            onDrop={handleDrop}
            onClick={() => fileInputRef.current?.click()}
            style={{
              border: `2px dashed ${isDragging ? "var(--accent, #3b82f6)" : "var(--border-color, #444)"}`,
              borderRadius: "8px",
              padding: "2rem 1.5rem",
              textAlign: "center",
              cursor: "pointer",
              marginBottom: "1.5rem",
              background: isDragging ? "rgba(59, 130, 246, 0.08)" : "transparent",
            }}
          >
            <i className="fa-solid fa-file-import fa-2x" style={{ marginBottom: "0.75rem", display: "block" }} />
            <p style={{ margin: 0, fontWeight: 500 }}>
              Click to select or drag and drop a workflow bundle (<code>.json</code>)
            </p>
            <p className="text-muted" style={{ margin: "0.5rem 0 0", fontSize: "0.85rem" }}>
              Standard WorkflowExportBundle schema version 1
            </p>
          </div>
        ) : (
          <div
            className="import-preview-box"
            style={{
              padding: "1rem",
              borderRadius: "6px",
              backgroundColor: "var(--bg-card, rgba(255, 255, 255, 0.05))",
              border: "1px solid var(--border-color, #444)",
              marginBottom: "1.5rem",
            }}
          >
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "0.75rem" }}>
              <span style={{ fontWeight: 600, fontSize: "0.95rem" }}>
                <i className="fa-solid fa-file-code" style={{ marginRight: "0.5rem" }} />
                {fileName || "bundle.json"}
              </span>
              <button
                type="button"
                className="btn btn-secondary btn-sm"
                onClick={() => {
                  setBundle(null);
                  setFileName("");
                }}
              >
                Choose Different File
              </button>
            </div>
            <div style={{ fontSize: "0.9rem", display: "grid", gap: "0.4rem" }}>
              <div>
                <strong>Workflow Name:</strong> {bundle.workflow.name}
              </div>
              {bundle.workflow.description ? (
                <div>
                  <strong>Description:</strong> {bundle.workflow.description}
                </div>
              ) : null}
              <div style={{ display: "flex", gap: "1.5rem", marginTop: "0.4rem" }}>
                <span>
                  <i className="fa-solid fa-circle-nodes" style={{ marginRight: "0.4rem" }} />
                  {bundle.states.length} {bundle.states.length === 1 ? "state" : "states"}
                </span>
                <span>
                  <i className="fa-solid fa-sliders" style={{ marginRight: "0.4rem" }} />
                  {bundle.workflow.variables?.length ?? 0} {bundle.workflow.variables?.length === 1 ? "variable" : "variables"}
                </span>
              </div>
            </div>
          </div>
        )}

        <div className="dialog-actions workflow-dialog-actions">
          <button type="button" className="btn btn-secondary" onClick={onClose} disabled={importing}>
            Cancel
          </button>
          <button
            type="submit"
            className="btn btn-primary"
            disabled={!bundle || importing}
          >
            {importing ? "Importing..." : "Import Workflow"}
          </button>
        </div>
      </form>
    </DialogShell>
  );
}
