import { useState, type ReactElement, type FormEvent } from "react";
import { DialogShell } from "./DialogShell";

export interface EdgeConfigModalProps {
  sourceName: string;
  targetName: string;
  initialDescription: string;
  onClose: () => void;
  onSave: (description: string) => void;
  onDelete: () => void;
}

export function EdgeConfigModal({
  sourceName,
  targetName,
  initialDescription,
  onClose,
  onSave,
  onDelete,
}: EdgeConfigModalProps): ReactElement {
  const [description, setDescription] = useState(initialDescription);

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    onSave(description.trim());
    onClose();
  };

  return (
    <DialogShell
      open={true}
      title="Edit Transition"
      titleId="edge-config-dialog-title"
      description={`Transition from ${sourceName} to ${targetName}`}
      onClose={onClose}
      closeLabel="Close transition dialog"
      className="message-dialog edge-config-dialog"
    >
      <form onSubmit={handleSubmit}>
        <div className="form-group" style={{ marginBottom: "1rem" }}>
          <label htmlFor="edge-desc-input" style={{ display: "block", marginBottom: "0.3rem", fontWeight: 600 }}>
            Condition / Description (optional)
          </label>
          <input
            id="edge-desc-input"
            type="text"
            className="form-control"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder="e.g. Tests pass, Review rejected, Success"
            autoFocus
          />
          <small className="text-muted" style={{ display: "block", marginTop: "0.3rem" }}>
            This label appears on the connection arrow in the visual canvas.
          </small>
        </div>

        <div className="dialog-actions workflow-dialog-actions" style={{ justifyContent: "space-between" }}>
          <button
            type="button"
            className="btn btn-danger"
            onClick={() => {
              onDelete();
              onClose();
            }}
          >
            <i className="fa-solid fa-trash" aria-hidden="true" /> Remove Connection
          </button>
          <div style={{ display: "flex", gap: "0.5rem" }}>
            <button type="button" className="btn btn-secondary" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" className="btn btn-primary">
              Save
            </button>
          </div>
        </div>
      </form>
    </DialogShell>
  );
}
