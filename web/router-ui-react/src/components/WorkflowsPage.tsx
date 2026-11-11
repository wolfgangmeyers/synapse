import { useState, useEffect, type ReactElement } from "react";
import type { RouterHttpClient } from "../transport/http-client";
import type { Workflow, WorkflowExportBundle } from "../domain/workflow-states";
import { DialogShell } from "./DialogShell";
import { WorkflowCanvas } from "./WorkflowCanvas";
import { ImportWorkflowModal } from "./ImportWorkflowModal";
import { WorkflowSchedulesView } from "./WorkflowSchedulesView";
import { WorkflowBackgroundRunModal } from "./WorkflowBackgroundRunModal";

export interface WorkflowsPageProps {
  client: RouterHttpClient;
  onNavigateHome?: () => void;
  onBack?: () => void;
}

export function WorkflowsPage({ client, onNavigateHome, onBack }: WorkflowsPageProps): ReactElement {
  const [activeTab, setActiveTab] = useState<"workflows" | "schedules">("workflows");
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | undefined>(undefined);
  const [selectedWorkflowId, setSelectedWorkflowId] = useState<string | null>(null);

  // Modal states for Workflow CRUD
  const [createDialogOpen, setCreateDialogOpen] = useState(false);
  const [newWorkflowName, setNewWorkflowName] = useState("");
  const [newWorkflowDesc, setNewWorkflowDesc] = useState("");
  const [newWorkflowSnoozeMinutes, setNewWorkflowSnoozeMinutes] = useState<number>(10);
  const [createError, setCreateError] = useState<string | undefined>(undefined);
  const [creating, setCreating] = useState(false);

  const [renameWorkflow, setRenameWorkflow] = useState<Workflow | null>(null);
  const [renameName, setRenameName] = useState("");
  const [renameDesc, setRenameDesc] = useState("");
  const [renameSnoozeMinutes, setRenameSnoozeMinutes] = useState<number>(10);
  const [renameError, setRenameError] = useState<string | undefined>(undefined);
  const [renaming, setRenaming] = useState(false);

  const [deleteWorkflowId, setDeleteWorkflowId] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);

  const [importDialogOpen, setImportDialogOpen] = useState(false);
  const [runModalWorkflowId, setRunModalWorkflowId] = useState<string | null>(null);

  // GitHub Sync state
  const [gitRepoUrl, setGitRepoUrl] = useState("");
  const [savedRepoUrl, setSavedRepoUrl] = useState("");
  const [syncLoading, setSyncLoading] = useState(false);
  const [savingTarget, setSavingTarget] = useState(false);
  const [confirmPull, setConfirmPull] = useState(false);
  const [syncWarning, setSyncWarning] = useState<string | null>(null);
  const [syncStatus, setSyncStatus] = useState<{ message: string; error?: boolean }>({ message: "" });

  const fetchWorkflows = async () => {
    try {
      setLoading(true);
      setError(undefined);
      const list = await client.listWorkflows();
      setWorkflows(Array.isArray(list) ? list : []);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to load workflows");
      setWorkflows([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void fetchWorkflows();
  }, []);

  // Load git sync target on mount
  useEffect(() => {
    if (!client || typeof client.getGitSyncTarget !== "function") return;
    let active = true;
    client.getGitSyncTarget("workflows").then((res) => {
      if (!active) return;
      if (res && res.repo_url) {
        setGitRepoUrl(res.repo_url);
        setSavedRepoUrl(res.repo_url);
      } else {
        setGitRepoUrl("");
        setSavedRepoUrl("");
      }
    }).catch(() => {});
    return () => { active = false; };
  }, [client]);

  const handleSaveRepoUrl = async (newUrl?: string): Promise<void> => {
    const urlToSave = (newUrl !== undefined ? newUrl : gitRepoUrl).trim();
    if (!client || typeof client.setGitSyncTarget !== "function" || savingTarget) return;
    if (urlToSave === savedRepoUrl) return;
    if (!urlToSave) {
      void handleClearRepo();
      return;
    }
    setSavingTarget(true);
    try {
      const res = await client.setGitSyncTarget("workflows", urlToSave);
      setGitRepoUrl(res.repo_url);
      setSavedRepoUrl(res.repo_url);
      setSyncStatus({ message: "Repository URL saved." });
    } catch (err) {
      setSyncStatus({ message: err instanceof Error ? err.message : "Failed to save repository URL.", error: true });
    } finally {
      setSavingTarget(false);
    }
  };

  const handleClearRepo = async (): Promise<void> => {
    if (!client || savingTarget || syncLoading) return;
    if (!savedRepoUrl && !gitRepoUrl) return;
    setSavingTarget(true);
    try {
      if (savedRepoUrl && typeof client.clearGitSyncTarget === "function") {
        await client.clearGitSyncTarget("workflows");
      }
      setGitRepoUrl("");
      setSavedRepoUrl("");
      setSyncStatus({ message: "Repository URL cleared." });
    } catch (err) {
      setSyncStatus({ message: err instanceof Error ? err.message : "Failed to clear repository URL.", error: true });
    } finally {
      setSavingTarget(false);
    }
  };

  const handlePullConfirm = async (): Promise<void> => {
    if (!client || typeof client.pullGitSync !== "function" || syncLoading) return;
    setConfirmPull(false);
    setSyncLoading(true);
    setSyncWarning(null);
    setSyncStatus({ message: "" });
    try {
      const res = await client.pullGitSync("workflows");
      if (res.exists === false) {
        setSyncWarning("No workflows.json found in this repository. Nothing was changed.");
      } else if (res.success === false) {
        setSyncStatus({ message: res.error || "Git pull failed.", error: true });
      } else {
        const count = typeof res.count === "number" ? res.count : 0;
        setSyncStatus({ message: `Successfully pulled ${count} workflow${count === 1 ? "" : "s"} from GitHub.` });
        await fetchWorkflows();
      }
    } catch (err) {
      setSyncStatus({ message: err instanceof Error ? err.message : "Git pull failed.", error: true });
    } finally {
      setSyncLoading(false);
    }
  };

  const handlePush = async (): Promise<void> => {
    if (!client || typeof client.pushGitSync !== "function" || syncLoading) return;
    setSyncLoading(true);
    setSyncWarning(null);
    setSyncStatus({ message: "" });
    try {
      const res = await client.pushGitSync("workflows");
      if (res.success === false) {
        setSyncStatus({ message: res.error || "Git push failed.", error: true });
      } else {
        const count = typeof res.count === "number" ? res.count : workflows.length;
        setSyncStatus({ message: `Successfully pushed ${count} workflow${count === 1 ? "" : "s"} to GitHub.` });
      }
    } catch (err) {
      setSyncStatus({ message: err instanceof Error ? err.message : "Git push failed.", error: true });
    } finally {
      setSyncLoading(false);
    }
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newWorkflowName.trim()) {
      setCreateError("Workflow name is required");
      return;
    }
    setCreating(true);
    setCreateError(undefined);
    try {
      const snoozeDurationMS = (newWorkflowSnoozeMinutes > 0 ? newWorkflowSnoozeMinutes : 10) * 60_000;
      const created = await client.createWorkflow({
        name: newWorkflowName.trim(),
        description: newWorkflowDesc.trim(),
        snooze_duration_ms: snoozeDurationMS,
      });
      setCreateDialogOpen(false);
      setNewWorkflowName("");
      setNewWorkflowDesc("");
      setNewWorkflowSnoozeMinutes(10);
      await fetchWorkflows();
      setSelectedWorkflowId(created.id);
    } catch (err: unknown) {
      setCreateError(err instanceof Error ? err.message : "Failed to create workflow");
    } finally {
      setCreating(false);
    }
  };

  const handleRename = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!renameWorkflow) return;
    if (!renameName.trim()) {
      setRenameError("Workflow name is required");
      return;
    }
    setRenaming(true);
    setRenameError(undefined);
    try {
      const snoozeDurationMS = (renameSnoozeMinutes > 0 ? renameSnoozeMinutes : 10) * 60_000;
      await client.updateWorkflow(renameWorkflow.id, {
        name: renameName.trim(),
        description: renameDesc.trim(),
        snooze_duration_ms: snoozeDurationMS,
      });
      setRenameWorkflow(null);
      await fetchWorkflows();
    } catch (err: unknown) {
      setRenameError(err instanceof Error ? err.message : "Failed to rename workflow");
    } finally {
      setRenaming(false);
    }
  };

  const handleExport = async (wf: Workflow) => {
    try {
      setError(undefined);
      const bundle = await client.exportWorkflow(wf.id);
      const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(bundle, null, 2));
      const downloadAnchor = document.createElement("a");
      const sanitizedName = wf.name.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-|-$/g, "") || "workflow";
      downloadAnchor.setAttribute("href", dataStr);
      downloadAnchor.setAttribute("download", `${sanitizedName}-workflow.json`);
      document.body.appendChild(downloadAnchor);
      downloadAnchor.click();
      downloadAnchor.remove();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to export workflow");
    }
  };

  const handleImport = async (bundle: WorkflowExportBundle) => {
    const imported = await client.importWorkflow(bundle);
    await fetchWorkflows();
    setSelectedWorkflowId(imported.id);
  };

  const handleDuplicate = async (wf: Workflow) => {
    try {
      setError(undefined);
      const dup = await client.duplicateWorkflow(wf.id);
      await fetchWorkflows();
      setSelectedWorkflowId(dup.id);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to duplicate workflow");
    }
  };

  const handleDelete = async () => {
    if (!deleteWorkflowId) return;
    setDeleting(true);
    try {
      await client.deleteWorkflow(deleteWorkflowId);
      setDeleteWorkflowId(null);
      await fetchWorkflows();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete workflow");
    } finally {
      setDeleting(false);
    }
  };

  const handleBack = () => {
    if (onBack) {
      onBack();
    } else if (onNavigateHome) {
      onNavigateHome();
    }
  };

  if (selectedWorkflowId) {
    return (
      <WorkflowCanvas
        workflowId={selectedWorkflowId}
        client={client}
        onBack={() => {
          setSelectedWorkflowId(null);
          void fetchWorkflows();
        }}
      />
    );
  }

  return (
    <div className="workflows-page">
      <header className="workflows-header">
        <div className="workflows-nav">
          <button type="button" className="workflows-back-btn" onClick={handleBack}>
            <i className="fa-solid fa-arrow-left" aria-hidden="true" /> Dashboard
          </button>
          <div className="workflows-title">
            <h2>Workflows</h2>
            <span className="badge">{workflows.length}</span>
          </div>
        </div>
        <div style={{ display: "flex", gap: "0.5rem" }}>
          {/* Top Navigation Tabs */}
          <div className="brand-header-tabs" style={{ maxWidth: 300, display: "flex", gap: "8px" }}>
            <button
              type="button"
              className={`brand-header-tab ${activeTab === "workflows" ? "active" : ""}`}
              onClick={() => setActiveTab("workflows")}
            >
              <i className="fa-solid fa-diagram-project" aria-hidden="true" style={{ marginRight: 6 }} /> Workflows
            </button>
            <button
              type="button"
              className={`brand-header-tab ${activeTab === "schedules" ? "active" : ""}`}
              onClick={() => setActiveTab("schedules")}
            >
              <i className="fa-solid fa-clock" aria-hidden="true" style={{ marginRight: 6 }} /> Schedules
            </button>
          </div>

          {activeTab === "workflows" ? (
            <>
              <button
                type="button"
                className="btn btn-secondary"
                onClick={() => setImportDialogOpen(true)}
              >
                <i className="fa-solid fa-file-import" aria-hidden="true" /> Import
              </button>
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => {
                  setNewWorkflowName("");
                  setNewWorkflowDesc("");
                  setCreateError(undefined);
                  setCreateDialogOpen(true);
                }}
              >
                <i className="fa-solid fa-plus" aria-hidden="true" /> New Workflow
              </button>
            </>
          ) : null}
        </div>
      </header>

      {error ? (
        <div className="alert alert-danger" role="alert">
          {error}
        </div>
      ) : null}

      {activeTab === "schedules" ? (
        <div className="workflows-content">
          <WorkflowSchedulesView client={client} />
        </div>
      ) : (
      <div className="workflows-content">
        <section className="workflows-github-sync" aria-label="GitHub Sync">
          <div className="workflows-github-sync-header">
            <i className="fa-brands fa-github" aria-hidden="true" />
            <span>GitHub Sync</span>
          </div>
          <div className="workflows-github-sync-row">
            <div className="workflows-github-input-wrap">
              <input
                type="text"
                className="workflows-github-url-input"
                placeholder="git@github.com:owner/repo.git or https://github.com/owner/repo.git"
                value={gitRepoUrl}
                disabled={syncLoading || savingTarget}
                onChange={(e) => setGitRepoUrl(e.target.value)}
                onBlur={() => { void handleSaveRepoUrl(); }}
                onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void handleSaveRepoUrl(); } }}
              />
              {gitRepoUrl || savedRepoUrl ? (
                <button
                  type="button"
                  className="workflows-github-input-clear"
                  onClick={() => void handleClearRepo()}
                  title="Clear repository URL"
                  aria-label="Clear repository URL"
                  disabled={syncLoading || savingTarget}
                >
                  <i className="fa-solid fa-xmark" aria-hidden="true" />
                </button>
              ) : null}
            </div>
            <button
              type="button"
              className="workflows-github-save-btn"
              disabled={!gitRepoUrl.trim() || gitRepoUrl.trim() === savedRepoUrl || syncLoading || savingTarget}
              onClick={() => void handleSaveRepoUrl()}
            >
              {savingTarget ? "Saving…" : "Save"}
            </button>
            <div className="workflows-github-sync-actions">
              <button
                type="button"
                className="icon-btn-round"
                title="Pull from GitHub"
                aria-label="Pull from GitHub"
                disabled={!savedRepoUrl || syncLoading || savingTarget}
                onClick={() => setConfirmPull(true)}
              >
                <i className={`fa-solid ${syncLoading ? "fa-spinner fa-spin" : "fa-cloud-arrow-down"}`} aria-hidden="true" />
              </button>
              <button
                type="button"
                className="icon-btn-round"
                title="Push to GitHub"
                aria-label="Push to GitHub"
                disabled={!savedRepoUrl || syncLoading || savingTarget}
                onClick={() => void handlePush()}
              >
                <i className={`fa-solid ${syncLoading ? "fa-spinner fa-spin" : "fa-cloud-arrow-up"}`} aria-hidden="true" />
              </button>
            </div>
          </div>
          {confirmPull ? (
            <div className="workflows-github-sync-confirm" role="alert">
              <p>This will replace all local workflows with the version from GitHub. Continue?</p>
              <div className="workflows-github-sync-confirm-actions">
                <button
                  type="button"
                  className="secondary"
                  disabled={syncLoading}
                  onClick={() => setConfirmPull(false)}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className="danger-button"
                  disabled={syncLoading}
                  onClick={() => void handlePullConfirm()}
                >
                  {syncLoading ? "Pulling…" : "Confirm Pull"}
                </button>
              </div>
            </div>
          ) : null}
          {syncWarning ? (
            <div className="workflows-github-sync-confirm" role="alert">
              <p>{syncWarning}</p>
              <div className="workflows-github-sync-confirm-actions">
                <button
                  type="button"
                  className="secondary"
                  onClick={() => setSyncWarning(null)}
                >
                  Dismiss
                </button>
              </div>
            </div>
          ) : null}
          {syncStatus.message ? (
            <p className={`prompt-template-dialog-status${syncStatus.error ? " error" : ""}`} role={syncStatus.error ? "alert" : "status"}>
              {syncStatus.message}
            </p>
          ) : null}
        </section>
        {loading ? (
          <div className="workflows-loading">Loading workflows...</div>
        ) : workflows.length === 0 ? (
          <div className="workflows-empty">
            <i className="fa-solid fa-diagram-project fa-3x" aria-hidden="true" />
            <p>No workflows yet.</p>
            <p className="text-muted">Create a workflow to model agent states and transitions visually.</p>
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => {
                setNewWorkflowName("");
                setNewWorkflowDesc("");
                setCreateError(undefined);
                setCreateDialogOpen(true);
              }}
            >
              Create Workflow
            </button>
          </div>
        ) : (
          <div className="workflows-grid">
            {workflows.map((wf) => (
              <div
                key={wf.id}
                className="workflow-card"
                onClick={() => setSelectedWorkflowId(wf.id)}
                role="button"
                tabIndex={0}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    setSelectedWorkflowId(wf.id);
                  }
                }}
              >
                <div className="workflow-card-header">
                  <div className="workflow-card-title-group">
                    <span className="workflow-card-name">{wf.name}</span>
                    {wf.description ? <p className="workflow-card-desc">{wf.description}</p> : null}
                  </div>
                  <div className="workflow-card-actions" onClick={(e) => e.stopPropagation()}>
                    <button
                      type="button"
                      className="icon-btn-round"
                      title="Edit / Rename"
                      aria-label="Edit or rename workflow"
                      onClick={() => {
                        setRenameWorkflow(wf);
                        setRenameName(wf.name);
                        setRenameDesc(wf.description ?? "");
                        setRenameSnoozeMinutes(wf.snooze_duration_ms ? Math.round(wf.snooze_duration_ms / 60_000) : 10);
                        setRenameError(undefined);
                      }}
                    >
                      <i className="fa-solid fa-pen-to-square" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="icon-btn-round"
                      title="Duplicate workflow"
                      aria-label="Duplicate workflow"
                      onClick={() => void handleDuplicate(wf)}
                    >
                      <i className="fa-solid fa-copy" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="icon-btn-round"
                      title="Export workflow"
                      aria-label="Export workflow"
                      onClick={() => void handleExport(wf)}
                    >
                      <i className="fa-solid fa-file-export" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="icon-btn-round danger"
                      title="Delete workflow"
                      aria-label="Delete workflow"
                      onClick={() => setDeleteWorkflowId(wf.id)}
                    >
                      <i className="fa-solid fa-trash" aria-hidden="true" />
                    </button>
                  </div>
                </div>

                <div className="workflow-card-meta">
                  <span className="workflow-state-count-badge">
                    <i className="fa-solid fa-circle-nodes" aria-hidden="true" />{" "}
                    {wf.state_count ?? 0} {wf.state_count === 1 ? "state" : "states"}
                  </span>
                  <span className="workflow-updated-at">
                    Updated {new Date(wf.updated_at).toLocaleDateString()}
                  </span>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
      )}

      {createDialogOpen ? (
        <DialogShell
          open={true}
          title="New Workflow"
          titleId="create-workflow-dialog-title"
          description="Create a new workflow canvas for modeling state transitions."
          onClose={() => setCreateDialogOpen(false)}
          closeLabel="Close create workflow dialog"
          className="message-dialog"
        >
          <form onSubmit={(e) => void handleCreate(e)}>
            {createError ? <div className="alert alert-danger">{createError}</div> : null}
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="workflow-name-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Workflow Name *
              </label>
              <input
                id="workflow-name-input"
                type="text"
                className="form-control"
                placeholder="e.g. Code Review Pipeline"
                value={newWorkflowName}
                onChange={(e) => setNewWorkflowName(e.target.value)}
                autoFocus
                required
              />
            </div>
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="workflow-desc-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Description (optional)
              </label>
              <input
                id="workflow-desc-input"
                type="text"
                className="form-control"
                placeholder="Brief summary of workflow goals"
                value={newWorkflowDesc}
                onChange={(e) => setNewWorkflowDesc(e.target.value)}
              />
            </div>
            <div className="form-group" style={{ marginBottom: "1.5rem" }}>
              <label htmlFor="workflow-snooze-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Maximum snooze (minutes)
              </label>
              <input
                id="workflow-snooze-input"
                type="number"
                min={1}
                className="form-control"
                placeholder="10"
                value={newWorkflowSnoozeMinutes}
                onChange={(e) => {
                  const val = parseInt(e.target.value, 10);
                  setNewWorkflowSnoozeMinutes(isNaN(val) ? 10 : Math.max(1, val));
                }}
              />
              <span style={{ fontSize: "0.8rem", color: "var(--muted, #888)", display: "block", marginTop: "0.25rem" }}>
                Default and maximum snooze for this workflow; agents may request a shorter wait (default: 10 minutes, maximum: 24 hours).
              </span>
            </div>
            <div className="dialog-actions workflow-dialog-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setCreateDialogOpen(false)}>
                Cancel
              </button>
              <button type="submit" className="btn btn-primary" disabled={creating}>
                {creating ? "Creating..." : "Create & Open Canvas"}
              </button>
            </div>
          </form>
        </DialogShell>
      ) : null}

      {renameWorkflow ? (
        <DialogShell
          open={true}
          title="Edit Workflow Details"
          titleId="rename-workflow-dialog-title"
          description="Update workflow name and description."
          onClose={() => setRenameWorkflow(null)}
          closeLabel="Close rename workflow dialog"
          className="message-dialog"
        >
          <form onSubmit={(e) => void handleRename(e)}>
            {renameError ? <div className="alert alert-danger">{renameError}</div> : null}
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="rename-name-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Workflow Name *
              </label>
              <input
                id="rename-name-input"
                type="text"
                className="form-control"
                value={renameName}
                onChange={(e) => setRenameName(e.target.value)}
                autoFocus
                required
              />
            </div>
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="rename-desc-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Description
              </label>
              <input
                id="rename-desc-input"
                type="text"
                className="form-control"
                value={renameDesc}
                onChange={(e) => setRenameDesc(e.target.value)}
              />
            </div>
            <div className="form-group" style={{ marginBottom: "1.5rem" }}>
              <label htmlFor="rename-snooze-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Maximum snooze (minutes)
              </label>
              <input
                id="rename-snooze-input"
                type="number"
                min={1}
                className="form-control"
                value={renameSnoozeMinutes}
                onChange={(e) => setRenameSnoozeMinutes(Math.max(1, parseInt(e.target.value, 10) || 1))}
              />
              <span style={{ fontSize: "0.8rem", color: "var(--muted, #888)", display: "block", marginTop: "0.25rem" }}>
                Default and maximum snooze for this workflow; agents may request a shorter wait (default: 10 minutes, maximum: 24 hours).
              </span>
            </div>
            <div className="dialog-actions workflow-dialog-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setRenameWorkflow(null)}>
                Cancel
              </button>
              <button type="submit" className="btn btn-primary" disabled={renaming}>
                {renaming ? "Saving..." : "Save Changes"}
              </button>
            </div>
          </form>
        </DialogShell>
      ) : null}

      {deleteWorkflowId ? (
        <DialogShell
          open={true}
          title="Delete Workflow"
          titleId="delete-workflow-dialog-title"
          description="Are you sure you want to delete this workflow and all its states? This cannot be undone."
          onClose={() => setDeleteWorkflowId(null)}
          closeLabel="Close delete workflow dialog"
          className="message-dialog delete-confirm-dialog"
        >
          <div className="dialog-actions workflow-dialog-actions">
            <button type="button" className="btn btn-secondary" onClick={() => setDeleteWorkflowId(null)}>
              Cancel
            </button>
            <button type="button" className="btn btn-danger" disabled={deleting} onClick={() => void handleDelete()}>
              {deleting ? "Deleting..." : "Delete Workflow"}
            </button>
          </div>
        </DialogShell>
      ) : null}

      <ImportWorkflowModal
        open={importDialogOpen}
        onClose={() => setImportDialogOpen(false)}
        onImport={handleImport}
      />
      {runModalWorkflowId ? (
        <WorkflowBackgroundRunModal
          open={Boolean(runModalWorkflowId)}
          client={client}
          initialWorkflowId={runModalWorkflowId}
          onClose={() => setRunModalWorkflowId(null)}
          onLaunched={() => {
            setRunModalWorkflowId(null);
            // Return home so user lands on dashboard / background workers
            if (onNavigateHome) {
              onNavigateHome();
            } else if (onBack) {
              onBack();
            } else {
              window.location.hash = "";
            }
          }}
        />
      ) : null}
    </div>
  );
}

export default WorkflowsPage;
