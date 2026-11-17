import { useState, useEffect, type ReactElement } from "react";
import type { RouterHttpClient, WorkflowCollection } from "../transport/http-client";
import "./WorkflowCollections.css";
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

  // Workflow Collections state
  const [collections, setCollections] = useState<WorkflowCollection[]>([]);
  const [activeCollectionId, setActiveCollectionId] = useState<string>("default");
  const [collectionModalOpen, setCollectionModalOpen] = useState(false);
  const [collectionModalMode, setCollectionModalMode] = useState<"list" | "create" | "edit">("list");
  const [newCollectionName, setNewCollectionName] = useState("");
  const [newCollectionDesc, setNewCollectionDesc] = useState("");
  const [creatingCollection, setCreatingCollection] = useState(false);
  const [createCollectionError, setCreateCollectionError] = useState<string | undefined>(undefined);
  const [editingCollection, setEditingCollection] = useState<WorkflowCollection | null>(null);
  const [editCollectionName, setEditCollectionName] = useState("");
  const [editCollectionDesc, setEditCollectionDesc] = useState("");
  const [updatingCollection, setUpdatingCollection] = useState(false);
  const [editCollectionError, setEditCollectionError] = useState<string | undefined>(undefined);
  const [deletingCollection, setDeletingCollection] = useState<WorkflowCollection | null>(null);
  const [deletingCollectionLoading, setDeletingCollectionLoading] = useState(false);
  const [deleteCollectionError, setDeleteCollectionError] = useState<string | undefined>(undefined);
  const [collectionModalError, setCollectionModalError] = useState<string | undefined>(undefined);

  // Move & Copy workflow states
  const [moveModalWorkflow, setMoveModalWorkflow] = useState<Workflow | null>(null);
  const [moveTargetCollectionId, setMoveTargetCollectionId] = useState<string>("");
  const [moving, setMoving] = useState(false);
  const [moveError, setMoveError] = useState<string | undefined>(undefined);

  const [copyModalWorkflow, setCopyModalWorkflow] = useState<Workflow | null>(null);
  const [copyTargetCollectionId, setCopyTargetCollectionId] = useState<string>("");
  const [copyWorkflowName, setCopyWorkflowName] = useState<string>("");
  const [copying, setCopying] = useState(false);
  const [copyError, setCopyError] = useState<string | undefined>(undefined);

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

  const fetchCollections = async () => {
    try {
      if (!client || typeof client.listWorkflowCollections !== "function") return;
      const list = await client.listWorkflowCollections();
      setCollections(Array.isArray(list) ? list : []);
    } catch (err: unknown) {
      console.error("Failed to load collections", err);
    }
  };

  const fetchWorkflows = async (colId = activeCollectionId) => {
    try {
      setLoading(true);
      setError(undefined);
      const list = await client.listWorkflows(colId);
      setWorkflows(Array.isArray(list) ? list : []);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to load workflows");
      setWorkflows([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void fetchCollections();
  }, []);

  useEffect(() => {
    void fetchWorkflows(activeCollectionId);
  }, [activeCollectionId]);

  // Derive git sync target for the active collection.
  // The default collection uses the legacy target key "workflows" for backward compatibility,
  // while user collections use "workflows:<collectionId>".
  const currentSyncTarget = activeCollectionId === "default" ? "workflows" : `workflows:${activeCollectionId}`;

  // Load git sync target on mount or active collection change
  useEffect(() => {
    setGitRepoUrl("");
    setSavedRepoUrl("");
    setSyncStatus({ message: "" });
    setSyncWarning(null);
    if (!client || typeof client.getGitSyncTarget !== "function") return;
    let active = true;
    client.getGitSyncTarget(currentSyncTarget).then((res) => {
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
  }, [client, currentSyncTarget]);

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
      const res = await client.setGitSyncTarget(currentSyncTarget, urlToSave);
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
        await client.clearGitSyncTarget(currentSyncTarget);
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
      const res = await client.pullGitSync(currentSyncTarget);
      if (res.exists === false) {
        setSyncWarning("No workflows.json found in this repository. Nothing was changed.");
      } else if (res.success === false) {
        setSyncStatus({ message: res.error || "Git pull failed.", error: true });
      } else {
        const count = typeof res.count === "number" ? res.count : 0;
        setSyncStatus({ message: `Successfully pulled ${count} workflow${count === 1 ? "" : "s"} from GitHub.` });
        await fetchWorkflows(activeCollectionId);
        await fetchCollections();
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
      const res = await client.pushGitSync(currentSyncTarget);
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
        collection_id: activeCollectionId,
      });
      setCreateDialogOpen(false);
      setNewWorkflowName("");
      setNewWorkflowDesc("");
      setNewWorkflowSnoozeMinutes(10);
      await fetchWorkflows(activeCollectionId);
      await fetchCollections();
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
      const dup = await client.duplicateWorkflow(wf.id, { destination_collection_id: activeCollectionId });
      await fetchWorkflows(activeCollectionId);
      await fetchCollections();
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
      await fetchWorkflows(activeCollectionId);
      await fetchCollections();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete workflow");
    } finally {
      setDeleting(false);
    }
  };


  const handleCreateCollection = async (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (!newCollectionName.trim()) {
      setCreateCollectionError("Collection name is required");
      return;
    }
    setCreatingCollection(true);
    setCreateCollectionError(undefined);
    try {
      const payload: { name: string; description?: string } = {
        name: newCollectionName.trim(),
      };
      if (newCollectionDesc.trim()) {
        payload.description = newCollectionDesc.trim();
      }
      const created = await client.createWorkflowCollection(payload);
      setNewCollectionName("");
      setNewCollectionDesc("");
      await fetchCollections();
      setActiveCollectionId(created.id);
      setCollectionModalMode("list");
    } catch (err: unknown) {
      setCreateCollectionError(err instanceof Error ? err.message : "Failed to create collection");
    } finally {
      setCreatingCollection(false);
    }
  };

  const handleUpdateCollection = async (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (!editingCollection) return;
    if (!editCollectionName.trim()) {
      setEditCollectionError("Collection name is required");
      return;
    }
    setUpdatingCollection(true);
    setEditCollectionError(undefined);
    try {
      const payload: { name: string; description?: string } = {
        name: editCollectionName.trim(),
      };
      if (editCollectionDesc.trim()) {
        payload.description = editCollectionDesc.trim();
      }
      await client.updateWorkflowCollection(editingCollection.id, payload);
      setEditingCollection(null);
      await fetchCollections();
      await fetchWorkflows(activeCollectionId);
      setCollectionModalMode("list");
    } catch (err: unknown) {
      setEditCollectionError(err instanceof Error ? err.message : "Failed to update collection");
    } finally {
      setUpdatingCollection(false);
    }
  };

  const handleConfirmDeleteCollection = async () => {
    if (!deletingCollection) return;
    setDeletingCollectionLoading(true);
    setDeleteCollectionError(undefined);
    try {
      // Pass cascade=true if collection contains workflows, triggering clean cascade deletion in backend
      await client.deleteWorkflowCollection(deletingCollection.id, deletingCollection.workflow_count > 0);
      if (deletingCollection.id === activeCollectionId) {
        setActiveCollectionId("default");
      }
      setDeletingCollection(null);
      await fetchCollections();
      await fetchWorkflows("default");
    } catch (err: unknown) {
      setDeleteCollectionError(err instanceof Error ? err.message : "Failed to delete collection");
    } finally {
      setDeletingCollectionLoading(false);
    }
  };

  const handleMoveWorkflow = async (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (!moveModalWorkflow || !moveTargetCollectionId) return;
    setMoving(true);
    setMoveError(undefined);
    try {
      await client.moveWorkflow(moveModalWorkflow.id, moveTargetCollectionId);
      setMoveModalWorkflow(null);
      await fetchWorkflows(activeCollectionId);
      await fetchCollections();
    } catch (err: unknown) {
      setMoveError(err instanceof Error ? err.message : "Failed to move workflow");
    } finally {
      setMoving(false);
    }
  };

  const handleCopyWorkflow = async (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (!copyModalWorkflow || !copyTargetCollectionId) return;
    setCopying(true);
    setCopyError(undefined);
    try {
      const payload: { destination_collection_id?: string; name?: string } = {
        destination_collection_id: copyTargetCollectionId,
      };
      if (copyWorkflowName.trim()) {
        payload.name = copyWorkflowName.trim();
      }
      await client.duplicateWorkflow(copyModalWorkflow.id, payload);
      setCopyModalWorkflow(null);
      await fetchWorkflows(activeCollectionId);
      await fetchCollections();
    } catch (err: unknown) {
      setCopyError(err instanceof Error ? err.message : "Failed to copy workflow");
    } finally {
      setCopying(false);
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
                {/* Collection Switcher Bar */}
        <div className="workflow-collections-bar" aria-label="Workflow collections">
          <div className="workflow-collection-pills" role="tablist" aria-label="Workflow Collections">
            <span className="workflow-collection-label" style={{ fontSize: "12.5px", fontWeight: 600, color: "var(--text-secondary)", display: "inline-flex", alignItems: "center", gap: "6px", marginRight: "4px" }}>
              <i className="fa-solid fa-folder-tree" aria-hidden="true" style={{ color: "var(--accent, #60a5fa)" }} />
              Collection:
            </span>
            {(collections.length > 0 ? collections : [{ id: "default", name: "Default", description: "", created_at: "", updated_at: "", workflow_count: workflows.length }]).map((col) => {
              const isActive = col.id === activeCollectionId;
              return (
                <button
                  key={col.id}
                  type="button"
                  role="tab"
                  aria-selected={isActive}
                  className={`workflow-collection-pill${isActive ? " active" : ""}`}
                  onClick={() => setActiveCollectionId(col.id)}
                  title={col.description ? `${col.name}: ${col.description}` : col.name}
                >
                  <span>{col.name}</span>
                  <span className="pill-count">{col.workflow_count ?? 0}</span>
                </button>
              );
            })}
          </div>
          <button
            type="button"
            className="workflow-collection-manage-btn"
            onClick={() => {
              setCollectionModalMode("list");
              setCollectionModalOpen(true);
            }}
          >
            <i className="fa-solid fa-gear" aria-hidden="true" />
            <span>Manage Collections</span>
          </button>
        </div>

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
                      title="Move to collection"
                      aria-label="Move workflow to another collection"
                      onClick={() => {
                        setMoveTargetCollectionId(collections.find(c => c.id !== (wf.collection_id || activeCollectionId))?.id || "");
                        setMoveError(undefined);
                        setMoveModalWorkflow(wf);
                      }}
                    >
                      <i className="fa-solid fa-arrow-right-arrow-left" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      className="icon-btn-round"
                      title="Copy to collection"
                      aria-label="Copy workflow to another collection"
                      onClick={() => {
                        setCopyTargetCollectionId(collections.find(c => c.id !== (wf.collection_id || activeCollectionId))?.id || (wf.collection_id || activeCollectionId));
                        setCopyWorkflowName("");
                        setCopyError(undefined);
                        setCopyModalWorkflow(wf);
                      }}
                    >
                      <i className="fa-solid fa-clone" aria-hidden="true" />
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

      {/* Manage Collections Modal */}
      {collectionModalOpen ? (
        <DialogShell
          open={true}
          title={
            collectionModalMode === "create"
              ? "New Collection"
              : collectionModalMode === "edit"
              ? "Edit Collection"
              : "Workflow Collections"
          }
          titleId="workflow-collections-dialog-title"
          description={
            collectionModalMode === "create"
              ? "Create a new collection to group related workflows."
              : collectionModalMode === "edit"
              ? "Update collection name and description."
              : "Manage and organize your workflow collections."
          }
          onClose={() => {
            setCollectionModalOpen(false);
            setCollectionModalMode("list");
            setDeletingCollection(null);
          }}
          closeLabel="Close collections dialog"
          className="message-dialog workflow-collections-dialog"
        >
          {collectionModalMode === "list" ? (
            <div className="workflow-collections-modal-content">
              {collectionModalError ? <div className="alert alert-danger">{collectionModalError}</div> : null}
              <div style={{ display: "flex", justifyContent: "flex-end", marginBottom: "12px" }}>
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => {
                    setNewCollectionName("");
                    setNewCollectionDesc("");
                    setCreateCollectionError(undefined);
                    setCollectionModalMode("create");
                  }}
                >
                  <i className="fa-solid fa-plus" aria-hidden="true" style={{ marginRight: "6px" }} />
                  New Collection
                </button>
              </div>

              <div className="workflow-collections-list">
                {collections.map((col) => {
                  const isDefault = col.id === "default";
                  return (
                    <div key={col.id} className="workflow-collection-card">
                      <div className="workflow-collection-info">
                        <div className="workflow-collection-name-row">
                          <span className="workflow-collection-name">{col.name}</span>
                          {isDefault ? (
                            <span className="badge" style={{ fontSize: "11px", padding: "2px 6px" }}>Default</span>
                          ) : null}
                          <span className="badge" style={{ fontSize: "11px", padding: "2px 6px" }}>
                            {col.workflow_count ?? 0} {col.workflow_count === 1 ? "workflow" : "workflows"}
                          </span>
                        </div>
                        {col.description ? <p className="workflow-collection-desc">{col.description}</p> : null}
                      </div>
                      <div className="workflow-collection-actions">
                        {!isDefault ? (
                          <>
                            <button
                              type="button"
                              className="icon-btn-round"
                              title="Edit collection"
                              aria-label={`Edit collection ${col.name}`}
                              onClick={() => {
                                setEditingCollection(col);
                                setEditCollectionName(col.name);
                                setEditCollectionDesc(col.description || "");
                                setEditCollectionError(undefined);
                                setCollectionModalMode("edit");
                              }}
                            >
                              <i className="fa-solid fa-pen-to-square" aria-hidden="true" />
                            </button>
                            <button
                              type="button"
                              className="icon-btn-round danger"
                              title="Delete collection"
                              aria-label={`Delete collection ${col.name}`}
                              onClick={() => {
                                setDeletingCollection(col);
                                setDeleteCollectionError(undefined);
                              }}
                            >
                              <i className="fa-solid fa-trash" aria-hidden="true" />
                            </button>
                          </>
                        ) : null}
                      </div>
                    </div>
                  );
                })}
              </div>

              {deletingCollection ? (
                <div className="workflows-github-sync-confirm" role="alert" style={{ marginTop: "16px" }}>
                  <p>
                    Are you sure you want to delete collection <strong>{deletingCollection.name}</strong>?
                    {deletingCollection.workflow_count > 0 ? (
                      <span style={{ display: "block", marginTop: "4px", color: "var(--danger, #ef4444)" }}>
                        Warning: This collection contains {deletingCollection.workflow_count} workflow{deletingCollection.workflow_count === 1 ? "" : "s"} which will also be permanently deleted!
                      </span>
                    ) : null}
                  </p>
                  {deleteCollectionError ? <div className="alert alert-danger" style={{ margin: "8px 0" }}>{deleteCollectionError}</div> : null}
                  <div className="workflows-github-sync-confirm-actions">
                    <button
                      type="button"
                      className="secondary"
                      disabled={deletingCollectionLoading}
                      onClick={() => setDeletingCollection(null)}
                    >
                      Cancel
                    </button>
                    <button
                      type="button"
                      className="danger-button"
                      disabled={deletingCollectionLoading}
                      onClick={() => void handleConfirmDeleteCollection()}
                    >
                      {deletingCollectionLoading ? "Deleting…" : "Confirm Delete"}
                    </button>
                  </div>
                </div>
              ) : null}

              <div className="dialog-actions workflow-dialog-actions" style={{ marginTop: "16px" }}>
                <button type="button" className="btn btn-secondary" onClick={() => setCollectionModalOpen(false)}>
                  Close
                </button>
              </div>
            </div>
          ) : collectionModalMode === "create" ? (
            <form onSubmit={(e) => void handleCreateCollection(e)}>
              {createCollectionError ? <div className="alert alert-danger">{createCollectionError}</div> : null}
              <div className="form-group" style={{ marginBottom: "1rem" }}>
                <label htmlFor="create-col-name-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                  Collection Name *
                </label>
                <input
                  id="create-col-name-input"
                  type="text"
                  className="form-control"
                  placeholder="e.g. CI/CD Pipelines"
                  value={newCollectionName}
                  onChange={(e) => setNewCollectionName(e.target.value)}
                  autoFocus
                  required
                />
              </div>
              <div className="form-group" style={{ marginBottom: "1.5rem" }}>
                <label htmlFor="create-col-desc-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                  Description (optional)
                </label>
                <input
                  id="create-col-desc-input"
                  type="text"
                  className="form-control"
                  placeholder="Purpose of this collection"
                  value={newCollectionDesc}
                  onChange={(e) => setNewCollectionDesc(e.target.value)}
                />
              </div>
              <div className="dialog-actions workflow-dialog-actions">
                <button type="button" className="btn btn-secondary" onClick={() => setCollectionModalMode("list")}>
                  Back
                </button>
                <button type="submit" className="btn btn-primary" disabled={creatingCollection}>
                  {creatingCollection ? "Creating..." : "Create Collection"}
                </button>
              </div>
            </form>
          ) : (
            <form onSubmit={(e) => void handleUpdateCollection(e)}>
              {editCollectionError ? <div className="alert alert-danger">{editCollectionError}</div> : null}
              <div className="form-group" style={{ marginBottom: "1rem" }}>
                <label htmlFor="edit-col-name-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                  Collection Name *
                </label>
                <input
                  id="edit-col-name-input"
                  type="text"
                  className="form-control"
                  value={editCollectionName}
                  onChange={(e) => setEditCollectionName(e.target.value)}
                  autoFocus
                  required
                />
              </div>
              <div className="form-group" style={{ marginBottom: "1.5rem" }}>
                <label htmlFor="edit-col-desc-input" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                  Description (optional)
                </label>
                <input
                  id="edit-col-desc-input"
                  type="text"
                  className="form-control"
                  value={editCollectionDesc}
                  onChange={(e) => setEditCollectionDesc(e.target.value)}
                />
              </div>
              <div className="dialog-actions workflow-dialog-actions">
                <button type="button" className="btn btn-secondary" onClick={() => setCollectionModalMode("list")}>
                  Back
                </button>
                <button type="submit" className="btn btn-primary" disabled={updatingCollection}>
                  {updatingCollection ? "Saving..." : "Save Changes"}
                </button>
              </div>
            </form>
          )}
        </DialogShell>
      ) : null}

      {/* Move Workflow Modal */}
      {moveModalWorkflow ? (
        <DialogShell
          open={true}
          title="Move Workflow to Collection"
          titleId="move-workflow-dialog-title"
          description={`Move "${moveModalWorkflow.name}" to another collection.`}
          onClose={() => setMoveModalWorkflow(null)}
          closeLabel="Close move workflow dialog"
          className="message-dialog"
        >
          <form onSubmit={(e) => void handleMoveWorkflow(e)}>
            {moveError ? <div className="alert alert-danger">{moveError}</div> : null}
            <div className="form-group" style={{ marginBottom: "1.5rem" }}>
              <label htmlFor="move-target-collection" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Destination Collection *
              </label>
              <select
                id="move-target-collection"
                className="form-control"
                value={moveTargetCollectionId}
                onChange={(e) => setMoveTargetCollectionId(e.target.value)}
                required
              >
                <option value="" disabled>Select destination collection</option>
                {collections
                  .filter((c) => c.id !== (moveModalWorkflow.collection_id || activeCollectionId))
                  .map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
              </select>
            </div>
            <div className="dialog-actions workflow-dialog-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setMoveModalWorkflow(null)}>
                Cancel
              </button>
              <button type="submit" className="btn btn-primary" disabled={moving || !moveTargetCollectionId}>
                {moving ? "Moving..." : "Move Workflow"}
              </button>
            </div>
          </form>
        </DialogShell>
      ) : null}

      {/* Copy Workflow Modal */}
      {copyModalWorkflow ? (
        <DialogShell
          open={true}
          title="Copy Workflow to Collection"
          titleId="copy-workflow-dialog-title"
          description={`Create a copy of "${copyModalWorkflow.name}" in a collection.`}
          onClose={() => setCopyModalWorkflow(null)}
          closeLabel="Close copy workflow dialog"
          className="message-dialog"
        >
          <form onSubmit={(e) => void handleCopyWorkflow(e)}>
            {copyError ? <div className="alert alert-danger">{copyError}</div> : null}
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="copy-target-collection" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                Destination Collection *
              </label>
              <select
                id="copy-target-collection"
                className="form-control"
                value={copyTargetCollectionId}
                onChange={(e) => setCopyTargetCollectionId(e.target.value)}
                required
              >
                <option value="" disabled>Select destination collection</option>
                {collections.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name} {c.id === (copyModalWorkflow.collection_id || activeCollectionId) ? "(Current)" : ""}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-group" style={{ marginBottom: "1.5rem" }}>
              <label htmlFor="copy-workflow-name" style={{ display: "block", marginBottom: "0.4rem", fontWeight: 500 }}>
                New Workflow Name (optional)
              </label>
              <input
                id="copy-workflow-name"
                type="text"
                className="form-control"
                placeholder={`${copyModalWorkflow.name} (Copy)`}
                value={copyWorkflowName}
                onChange={(e) => setCopyWorkflowName(e.target.value)}
              />
              <span style={{ fontSize: "0.8rem", color: "var(--muted, #888)", display: "block", marginTop: "0.25rem" }}>
                Leave blank to automatically generate a unique name.
              </span>
            </div>
            <div className="dialog-actions workflow-dialog-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setCopyModalWorkflow(null)}>
                Cancel
              </button>
              <button type="submit" className="btn btn-primary" disabled={copying || !copyTargetCollectionId}>
                {copying ? "Copying..." : "Copy Workflow"}
              </button>
            </div>
          </form>
        </DialogShell>
      ) : null}
    </div>
  );
}

export default WorkflowsPage;
