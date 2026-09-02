import { useEffect, useState } from "react";
import { DialogShell } from "./DialogShell";

export interface InlineToolSettingsDialogProps {
  open: boolean;
  peer: string;
  enabled: boolean;
  capabilityAvailable: boolean;
  pending?: boolean;
  error?: string;
  returnFocus?: HTMLElement | null;
  onClose: () => void;
  onSave: (enabled: boolean) => void;
}

export function InlineToolSettingsDialog({ open, peer, enabled, capabilityAvailable, pending = false, error, returnFocus, onClose, onSave }: InlineToolSettingsDialogProps) {
  const [draft, setDraft] = useState(enabled);
  useEffect(() => { if (open) setDraft(enabled); }, [open, enabled]);
  const unavailable = !capabilityAvailable;
  return <DialogShell open={open} title={`Inline tool settings for ${peer}`} titleId="inline-tool-settings-dialog-title" description="Control top-level bash, edit, and write calls." onClose={onClose} closeLabel="Close inline tool settings" className="message-dialog inline-tool-settings-dialog" {...(returnFocus === undefined ? {} : { returnFocus })}>
    <div className="inline-tool-settings-form">
      <Toggle label="Allow inline tools" pressed={draft} disabled={unavailable} onChange={setDraft} />
      <p className="inline-tool-settings-warning">This allows top-level bash, edit, and write calls. Use it only when needed.</p>
      {unavailable ? <p className="inline-tool-settings-status" role="status">Inline tool control is not available for this worker.</p> : null}
      {error ? <p className="inline-tool-settings-status error" role="alert">{error}</p> : null}
      <div className="inline-tool-settings-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="button" disabled={unavailable || pending} onClick={() => onSave(draft)}>{pending ? "Saving…" : "Save"}</button></div>
    </div>
  </DialogShell>;
}

function Toggle({ label, pressed, disabled = false, onChange }: { label: string; pressed: boolean; disabled?: boolean; onChange: (value: boolean) => void }) {
  return <button type="button" className="goal-toggle" aria-pressed={pressed} aria-label={`${label}: ${pressed ? "ON" : "OFF"}`} disabled={disabled} onClick={() => onChange(!pressed)}>
    <span className="goal-toggle-label">{label}</span><span className="goal-toggle-track" aria-hidden="true"><span className="goal-toggle-thumb"><span className="goal-toggle-state">{pressed ? "ON" : "OFF"}</span></span></span>
  </button>;
}
