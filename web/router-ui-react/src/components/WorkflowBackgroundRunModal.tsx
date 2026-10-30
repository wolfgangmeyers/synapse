import { useState, useEffect, useMemo, useRef, useId, useLayoutEffect, type CSSProperties, type ReactElement, type FormEvent } from "react";
import { createPortal } from "react-dom";
import { DialogShell } from "./DialogShell";
import { ManagedDirectoryPicker } from "./ManagedDirectoryPicker";
import { loadManagedRecentFolders, recordManagedRecentFolder, saveManagedRecentFolders } from "../persistence/managed-folder-history";
import type { RouterHttpClient } from "../transport/http-client";
import type { Workflow } from "../domain/workflow-states";
import type { ManagedAgent } from "../protocol/types";

export interface WorkflowBackgroundRunModalProps {
  open: boolean;
  client: RouterHttpClient;
  initialWorkflowId?: string;
  recentFolders?: readonly string[];
  onListDirectories?: (path: string) => Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
  onClose: () => void;
  onLaunched?: (agent: ManagedAgent) => void;
}

export function WorkflowBackgroundRunModal({
  open,
  client,
  initialWorkflowId,
  recentFolders: propRecentFolders,
  onListDirectories,
  onClose,
  onLaunched,
}: WorkflowBackgroundRunModalProps): ReactElement | null {
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  // Must be unselected by default unless an initialWorkflowId is explicitly provided
  const [selectedWorkflowId, setSelectedWorkflowId] = useState<string>(initialWorkflowId ?? "");
  const [workflowSearchQuery, setWorkflowSearchQuery] = useState<string>("");
  const [dropdownOpen, setDropdownOpen] = useState<boolean>(false);
  const [menuHost, setMenuHost] = useState<HTMLElement>();
  const [menuPosition, setMenuPosition] = useState<CSSProperties>();

  const [targetCwd, setTargetCwd] = useState<string>("");
  const [variables, setVariables] = useState<Record<string, string>>({});
  const [loadingWorkflows, setLoadingWorkflows] = useState<boolean>(false);
  const [launching, setLaunching] = useState<boolean>(false);
  const [error, setError] = useState<string | null>(null);

  const fallbackRecentFolders = useMemo(() => loadManagedRecentFolders(), []);
  const activeRecentFolders = propRecentFolders ?? fallbackRecentFolders;

  const handleListDirectories = onListDirectories ?? ((path: string) => client.managedDirectories(path));

  const buttonRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listId = useId();

  useEffect(() => {
    if (!open) return;
    setLoadingWorkflows(true);
    setError(null);
    client
      .listWorkflows()
      .then((list) => {
        const wfList = Array.isArray(list) ? list : [];
        setWorkflows(wfList);
        if (initialWorkflowId && wfList.some((w) => w.id === initialWorkflowId)) {
          setSelectedWorkflowId(initialWorkflowId);
        } else {
          // Strictly unselected by default
          setSelectedWorkflowId("");
        }
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : "Failed to load workflows");
      })
      .finally(() => {
        setLoadingWorkflows(false);
      });
  }, [open, initialWorkflowId, client]);

  // Synchronize initial variables when selected workflow changes
  useEffect(() => {
    if (!selectedWorkflowId) {
      setVariables({});
      return;
    }
    const wf = workflows.find((w) => w.id === selectedWorkflowId);
    if (wf && wf.variables) {
      setVariables((prev) => {
        const next: Record<string, string> = { ...prev };
        for (const v of wf.variables || []) {
          if (next[v.name] === undefined) {
            next[v.name] = v.default_value ?? "";
          }
        }
        return next;
      });
    }
  }, [selectedWorkflowId, workflows]);

  const currentWorkflow = workflows.find((w) => w.id === selectedWorkflowId);

  const filteredWorkflows = useMemo(() => {
    const q = workflowSearchQuery.trim().toLowerCase();
    if (!q) return workflows;
    return workflows.filter(
      (w) =>
        w.name.toLowerCase().includes(q) ||
        w.id.toLowerCase().includes(q) ||
        (w.description && w.description.toLowerCase().includes(q))
    );
  }, [workflows, workflowSearchQuery]);

  // Focus search input when dropdown opens
  useEffect(() => {
    if (!dropdownOpen) return;
    requestAnimationFrame(() => searchRef.current?.focus());
  }, [dropdownOpen]);

  // Positioning portal exactly like ModelPicker
  useLayoutEffect(() => {
    if (!dropdownOpen || !buttonRef.current) {
      setMenuHost(undefined);
      setMenuPosition(undefined);
      return;
    }
    const trigger = buttonRef.current;
    const dialog = trigger.closest("dialog");
    // If inside a native <dialog>, portaling inside the dialog still clips if the dialog has overflow or bottom edge.
    // Instead, portal to document.body, or if in dialog, ensure the dialog has overflow: visible or portal to document.body!
    // But HTML <dialog> modal elements show in top layer: elements outside the <dialog> in document.body
    // render UNDER the dialog's ::backdrop unless inside the dialog.
    // However, when portaled inside dialog, the dialog has overflow: auto / hidden or bounds clipping.
    // Setting overflow: visible on the dialog prevents clipping the popover!
    setMenuHost(dialog ?? document.body);
    const positionMenu = () => {
      const rect = trigger.getBoundingClientRect();
      const dialogRect = dialog?.getBoundingClientRect();

      const width = rect.width;
      const left = dialogRect ? rect.left - dialogRect.left : rect.left;
      const spaceBelow = window.innerHeight - rect.bottom - 16;
      const spaceAbove = rect.top - 16;
      const above = spaceBelow < 240 && spaceAbove > spaceBelow;

      setMenuPosition({
        position: dialogRect ? "absolute" : "fixed",
        zIndex: 1000,
        left,
        right: "auto",
        width,
        ...(above
          ? {
              top: "auto",
              bottom: dialogRect ? dialogRect.bottom - rect.top + 8 : window.innerHeight - rect.top + 8,
            }
          : {
              top: dialogRect ? rect.bottom - dialogRect.top + 8 : rect.bottom + 8,
              bottom: "auto",
            }),
        maxHeight: Math.max(120, Math.min(260, (above ? spaceAbove : spaceBelow) - 8)),
      });
    };
    positionMenu();
    window.addEventListener("resize", positionMenu);
    window.addEventListener("scroll", positionMenu, true);
    window.visualViewport?.addEventListener("resize", positionMenu);
    window.visualViewport?.addEventListener("scroll", positionMenu);
    return () => {
      window.removeEventListener("resize", positionMenu);
      window.removeEventListener("scroll", positionMenu, true);
      window.visualViewport?.removeEventListener("resize", positionMenu);
      window.visualViewport?.removeEventListener("scroll", positionMenu);
    };
  }, [dropdownOpen]);

  const closeDropdown = () => {
    setDropdownOpen(false);
    setWorkflowSearchQuery("");
    buttonRef.current?.focus();
  };

  const chooseWorkflow = (wf: Workflow) => {
    setSelectedWorkflowId(wf.id);
    closeDropdown();
  };

  if (!open) return null;

  const handleSubmit = async (e: FormEvent): Promise<void> => {
    e.preventDefault();
    if (!selectedWorkflowId || launching) return;

    // Validate required variables
    if (currentWorkflow?.variables) {
      for (const v of currentWorkflow.variables) {
        const val = variables[v.name];
        if (v.required && (!val || val.trim() === "")) {
          setError(`Required parameter "${v.label || v.name}" must not be empty.`);
          return;
        }
      }
    }

    setLaunching(true);
    setError(null);
    try {
      const runOptions: { variables?: Record<string, string>; cwd?: string } = { variables };
      if (targetCwd.trim()) {
        runOptions.cwd = targetCwd.trim();
        saveManagedRecentFolders(undefined, recordManagedRecentFolder(activeRecentFolders, targetCwd.trim()));
      }
      const res = await client.runWorkflow(selectedWorkflowId, runOptions);
      onLaunched?.(res);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to launch workflow in background");
    } finally {
      setLaunching(false);
    }
  };

  const menu = dropdownOpen && menuHost ? (
    <div
      id={listId}
      className="model-menu model-menu-portal"
      style={menuPosition}
      role="dialog"
      aria-label="Choose workflow template"
      onKeyDown={(event) => {
        if (event.key === "Escape") closeDropdown();
      }}
    >
      <input
        ref={searchRef}
        className="model-search"
        type="search"
        aria-label="Search workflow templates"
        placeholder="Search workflows…"
        value={workflowSearchQuery}
        onChange={(event) => setWorkflowSearchQuery(event.target.value)}
      />
      <div className="model-options" role="listbox" aria-busy={loadingWorkflows}>
        {loadingWorkflows ? (
          <p className="model-options-empty">Loading workflows…</p>
        ) : filteredWorkflows.length ? (
          filteredWorkflows.map((w) => (
            <button
              key={w.id}
              type="button"
              className="model-option"
              role="option"
              aria-selected={w.id === selectedWorkflowId}
              onClick={() => chooseWorkflow(w)}
            >
              <strong>{w.name}</strong>
              {w.description ? <small>{w.description}</small> : null}
            </button>
          ))
        ) : (
          <p className="model-options-empty">No matching workflows</p>
        )}
      </div>
    </div>
  ) : null;

  return (
    <DialogShell
      open={open}
      title="Launch Background Workflow"
      titleId="launch-bg-workflow-title"
      description="Run an automated background agent executing this workflow."
      onClose={onClose}
      className="message-dialog workflow-bg-run-dialog"
    >
      <form onSubmit={(e) => { void handleSubmit(e); }} style={{ display: "flex", flexDirection: "column", gap: "1.1rem" }}>
        {error ? (
          <div className="alert alert-danger" role="alert">
            {error}
          </div>
        ) : null}

        {/* Model-style Searchable Workflow Dropdown */}
        <div className="form-group">
          <label style={{ display: "block", marginBottom: "6px", fontWeight: 600 }}>
            Workflow Template *
          </label>
          <div className="model-picker" onKeyDown={(event) => { if (event.key === "Escape" && dropdownOpen) closeDropdown(); }}>
            <button
              ref={buttonRef}
              type="button"
              className="model-current"
              style={{ width: "100%", justifyContent: "space-between", height: "40px", padding: "0 12px" }}
              aria-haspopup="dialog"
              aria-expanded={dropdownOpen}
              aria-controls={listId}
              disabled={loadingWorkflows || launching}
              onClick={() => setDropdownOpen((v) => !v)}
            >
              <span style={{ color: currentWorkflow ? "var(--text, #f8fafc)" : "var(--muted, #94a3b8)" }}>
                {currentWorkflow ? currentWorkflow.name : "Select a workflow…"}
              </span>
              <i className="fa-solid fa-chevron-down" aria-hidden="true" />
            </button>
          </div>
          {menu && menuHost ? createPortal(menu, menuHost) : null}
        </div>

        {/* Only when a workflow is selected do we render the parameters and optional cwd */}
        {selectedWorkflowId ? (
          <>
            <div className="form-group">
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

            {/* Dynamic Workflow Parameters */}
            {currentWorkflow?.variables && currentWorkflow.variables.length > 0 ? (
              <div className="form-group">
                <label style={{ fontWeight: 600, color: "var(--text-bright, #fff)", display: "block", marginBottom: "6px" }}>
                  Workflow Parameters ({currentWorkflow.variables.length})
                </label>
                <div
                  style={{
                    display: "flex",
                    flexDirection: "column",
                    gap: "10px",
                    padding: "12px",
                    background: "rgba(13, 20, 36, 0.45)",
                    borderRadius: "8px",
                    border: "1px solid rgba(255, 255, 255, 0.08)",
                  }}
                >
                  {currentWorkflow.variables.map((v) => (
                    <div key={v.name} className="variable-input-field">
                      <label
                        htmlFor={`run-var-${v.name}`}
                        style={{
                          fontSize: "0.85rem",
                          display: "flex",
                          justifyContent: "space-between",
                          marginBottom: "4px",
                        }}
                      >
                        <span>
                          {v.label || v.name} {v.required ? <span style={{ color: "var(--danger, #ef4444)" }}>*</span> : null}
                        </span>
                        <code style={{ fontSize: "0.75rem", color: "var(--muted)" }}>{v.name}</code>
                      </label>
                      {v.description ? (
                        <div style={{ fontSize: "0.75rem", color: "var(--muted)", marginBottom: "4px" }}>
                          {v.description}
                        </div>
                      ) : null}
                      {v.type === "textarea" ? (
                        <textarea
                          id={`run-var-${v.name}`}
                          className="form-control"
                          rows={3}
                          value={variables[v.name] ?? ""}
                          onChange={(e) =>
                            setVariables((prev) => ({
                              ...prev,
                              [v.name]: e.target.value,
                            }))
                          }
                          placeholder={v.default_value ? `Default: ${v.default_value}` : ""}
                          required={v.required}
                          disabled={launching}
                        />
                      ) : (
                        <input
                          id={`run-var-${v.name}`}
                          type="text"
                          className="form-control"
                          value={variables[v.name] ?? ""}
                          onChange={(e) =>
                            setVariables((prev) => ({
                              ...prev,
                              [v.name]: e.target.value,
                            }))
                          }
                          placeholder={v.default_value ? `Default: ${v.default_value}` : ""}
                          required={v.required}
                          disabled={launching}
                        />
                      )}
                    </div>
                  ))}
                </div>
              </div>
            ) : null}
          </>
        ) : null}

        <div className="dialog-actions" style={{ display: "flex", gap: "8px", justifyContent: "flex-end", marginTop: "1rem" }}>
          <button type="button" className="btn btn-secondary" onClick={onClose} disabled={launching}>
            Cancel
          </button>
          <button type="submit" className="btn btn-primary" disabled={launching || !selectedWorkflowId}>
            {launching ? (
              <>
                <i className="fa-solid fa-arrows-rotate fa-spin" aria-hidden="true" /> Launching...
              </>
            ) : (
              <>
                <i className="fa-solid fa-play" aria-hidden="true" /> Launch
              </>
            )}
          </button>
        </div>
      </form>
    </DialogShell>
  );
}

