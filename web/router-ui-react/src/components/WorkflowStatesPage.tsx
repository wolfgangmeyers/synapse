import { useState, useEffect, type ReactElement } from "react";
import type { RouterHttpClient } from "../transport/http-client";
import type { WorkflowState, WorkflowStateInput } from "../domain/workflow-states";

export interface WorkflowStatesPageProps {
  client: RouterHttpClient;
  onNavigateHome?: () => void;
  onBack?: () => void;
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
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | undefined>(undefined);
  const [deleteConfirmId, setDeleteConfirmId] = useState<string | undefined>(undefined);

  const fetchStates = async () => {
    try {
      setLoading(true);
      setError(undefined);
      const res = await client.workflowStates();
      setStates(res.workflow_states);
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
    setFormError(undefined);
    setIsCreating(true);
  };

  const openEdit = (state: WorkflowState) => {
    setEditingState(state);
    setFormName(state.name);
    setFormDescription(state.description);
    setFormEntryText(state.entry_text);
    setFormError(undefined);
    setIsCreating(false);
  };

  const closeForm = () => {
    setEditingState(undefined);
    setIsCreating(false);
    setFormError(undefined);
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!formName.trim()) {
      setFormError("State name is required");
      return;
    }
    setSaving(true);
    setFormError(undefined);
    const input: WorkflowStateInput = {
      name: formName.trim(),
      description: formDescription.trim(),
      entry_text: formEntryText,
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

      {/* Create / Edit Modal */}
      {isCreating || editingState ? (
        <div className="modal-backdrop" role="dialog" aria-modal="true" aria-labelledby="state-form-title">
          <div className="modal-content">
            <h3 id="state-form-title">{editingState ? "Edit Workflow State" : "New Workflow State"}</h3>
            <form onSubmit={handleSave}>
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
                  autoFocus
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
                  rows={6}
                  placeholder="Text that automatically posts to the conversation when entering this state..."
                  value={formEntryText}
                  onChange={(e) => setFormEntryText(e.target.value)}
                />
                <small className="form-text text-muted">
                  This text will automatically post to the agent when entering this state.
                </small>
              </div>
              <div className="modal-actions">
                <button type="button" className="btn btn-secondary" onClick={closeForm} disabled={saving}>
                  Cancel
                </button>
                <button type="submit" className="btn btn-primary" disabled={saving}>
                  {saving ? "Saving..." : editingState ? "Save Changes" : "Create State"}
                </button>
              </div>
            </form>
          </div>
        </div>
      ) : null}

      {/* Delete Confirmation Modal */}
      {deleteConfirmId ? (
        <div className="modal-backdrop" role="dialog" aria-modal="true">
          <div className="modal-content delete-confirm-modal">
            <h3>Delete Workflow State</h3>
            <p>Are you sure you want to delete this state? This action cannot be undone.</p>
            <div className="modal-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setDeleteConfirmId(undefined)}>
                Cancel
              </button>
              <button
                type="button"
                className="btn btn-danger"
                onClick={() => handleDelete(deleteConfirmId)}
              >
                Delete
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}

export { WorkflowStatesPage as WorkflowsPage };
export default WorkflowStatesPage;

