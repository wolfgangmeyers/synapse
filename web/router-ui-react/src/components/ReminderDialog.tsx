import { useEffect, useRef, useState } from "react";
import { activeRemindersForPeer, createReminderPayload, lockResultState, reminderLockKey, reminderRecurrenceLabel, validateReminderDelay, type Reminder, type ReminderCreateInput, type ReminderLockAction } from "../domain/reminders";
import { type PromptTemplate } from "../domain/prompt-templates";
import { PromptControls } from "./PromptControls";
import { DialogShell } from "./DialogShell";
import { useCountdown } from "../hooks/use-countdown";

export type ReminderDialogMode = "list" | "create" | "edit";

export interface ReminderDialogState {
  mode: ReminderDialogMode;
  editing?: Reminder | undefined;
  subject: string;
  body: string;
  delay: number;
  recurring: boolean;
  lockAfterCreate: boolean;
}

export interface ReminderDialogProps {
  open: boolean;
  peer: string;
  creator: string;
  reminders: Reminder[];
  locks?: Map<string, boolean> | Record<string, boolean>;
  pendingIds?: ReadonlySet<string>;
  status?: string;
  onClose: () => void;
  onCreate: (payload: ReminderCreateInput, lockAfterCreate: boolean) => void | Promise<void>;
  onUpdate: (id: string, payload: { subject: string | null; body: string }) => void | Promise<void>;
  onDelete: (id: string) => void | Promise<void>;
  onLock: (reminder: Reminder, action: ReminderLockAction) => void;
  templates?: PromptTemplate[];
  templatesLoaded?: boolean;
  onUseTemplate?: (template: PromptTemplate, selection: { start: number; end: number }) => void;
  onManageTemplates?: () => void;
  dialogState?: ReminderDialogState;
  onDialogStateChange?: (state: ReminderDialogState) => void;
}

function initialState(): ReminderDialogState { return { mode: "list", subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }; }
function lockFor(locks: ReminderDialogProps["locks"], key: string): boolean { return locks instanceof Map ? locks.get(key) === true : locks?.[key] === true; }

export function ReminderDialog({ open, peer, creator, reminders, locks, pendingIds = new Set<string>(), status, onClose, onCreate, onUpdate, onDelete, onLock, templates, templatesLoaded = true, onUseTemplate, onManageTemplates, dialogState, onDialogStateChange }: ReminderDialogProps) {
  const [localState, setLocalState] = useState<ReminderDialogState>(initialState);
  const state = dialogState ?? localState;
  const [error, setError] = useState("");
  const [busyId, setBusyId] = useState<string>();
  const reminderInput = useRef<HTMLTextAreaElement>(null);
  const updateState = (patch: Partial<ReminderDialogState>): void => {
    const next = { ...state, ...patch };
    if (dialogState === undefined) setLocalState(next);
    onDialogStateChange?.(next);
  };
  useEffect(() => {
    if (!open) {
      setError("");
      setBusyId(undefined);
      if (dialogState === undefined) setLocalState(initialState());
    }
  }, [dialogState, open]);
  const active = activeRemindersForPeer(reminders, peer);
  const startCreate = () => { updateState({ mode: "create", editing: undefined, subject: "", body: "", delay: 1, recurring: false, lockAfterCreate: false }); setError(""); };
  const startEdit = (reminder: Reminder) => { updateState({ mode: "edit", editing: reminder, subject: reminder.subject ?? "", body: reminder.body }); setError(""); };
  const submit = async (event: React.FormEvent<HTMLFormElement>): Promise<void> => {
    event.preventDefault(); const trimmed = state.body.trim();
    if (!trimmed) { setError("Enter reminder text."); return; }
    if (state.editing) {
      setBusyId(state.editing.id); setError("");
      try { await onUpdate(state.editing.id, { subject: state.subject.trim() || null, body: trimmed }); updateState({ mode: "list", editing: undefined }); }
      catch (cause) { setError(cause instanceof Error ? cause.message : "Reminder could not be updated."); }
      finally { setBusyId(undefined); }
      return;
    }
    if (!validateReminderDelay(state.delay)) { setError("Enter a delay from 1 to 10080 minutes."); return; }
    const options = { recurring: state.recurring, ...(state.subject.trim() ? { subject: state.subject.trim() } : {}) };
    try { await onCreate(createReminderPayload(peer, creator, trimmed, state.delay, options), state.lockAfterCreate); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Reminder could not be created."); }
  };
  const title = state.mode === "list" ? "Reminders" : state.mode === "edit" ? "Edit reminder" : "New reminder";
  return <DialogShell open={open} title={title} titleId="reminders-dialog-title" description="Scheduled reminders for this agent." onClose={onClose} closeLabel="Close reminders" className="message-dialog">
    {state.mode === "list" ? <div className="reminders-dialog-body"><button type="button" onClick={startCreate}>Add reminder</button>{error || status ? <p className="reminder-editor-status error" role={error ? "alert" : "status"}>{error || status}</p> : null}{active.length === 0 ? <p className="empty-state">No active reminders.</p> : active.map((reminder) => {
      const key = reminderLockKey(peer, reminder.id); const locked = lockFor(locks, key); const pending = pendingIds.has(reminder.id) || busyId === reminder.id;
      const remove = async (): Promise<void> => { setBusyId(reminder.id); setError(""); try { await onDelete(reminder.id); } catch (cause) { setError(cause instanceof Error ? cause.message : "Reminder could not be deleted."); } finally { setBusyId(undefined); } };
      return <article className="reminder-list-item" key={reminder.id}><strong>{reminder.subject || "Reminder"}</strong><p>{reminder.body}</p><ReminderCountdown nextDueAt={reminder.next_due_at} /><small className="reminder-recurrence">{reminderRecurrenceLabel(reminder.interval_ms)}</small><small className="reminder-lock-status">{locked ? "Native edits blocked." : ""}</small><div className="reminder-lock-actions"><button type="button" className="secondary" disabled={pending} onClick={() => startEdit(reminder)}>Edit</button><button type="button" className="secondary" disabled={pending} onClick={() => { void remove(); }}>Delete</button><button type="button" className="secondary" aria-pressed={locked} disabled={pending} onClick={() => onLock(reminder, locked ? "clear" : "set")}>{locked ? "Unlock" : "Lock"}</button></div></article>;
    })}</div> : <form className="reminder-editor-form" noValidate onSubmit={(event) => { void submit(event); }}><label>Subject<input value={state.subject} placeholder="Optional" onChange={(event) => updateState({ subject: event.target.value })} /></label><label>Reminder<textarea ref={reminderInput} aria-label="Reminder" value={state.body} placeholder="What should be sent?" rows={5} onChange={(event) => { updateState({ body: event.target.value }); setError(""); }} />{templates && onUseTemplate ? <PromptControls templates={templates} loaded={templatesLoaded} onManage={onManageTemplates ?? (() => undefined)} onUse={(template) => { const start = reminderInput.current?.selectionStart ?? state.body.length; const end = reminderInput.current?.selectionEnd ?? start; onUseTemplate(template, { start, end }); }} /> : null}</label>{state.mode === "create" ? <><label>Send after (minutes)<input type="number" min={1} max={10080} step={1} value={state.delay} onChange={(event) => updateState({ delay: Number(event.target.value) })} /></label><Toggle label="Repeat every delay interval" pressed={state.recurring} onChange={(recurring) => updateState({ recurring })} /><Toggle label="Reminder lock" pressed={state.lockAfterCreate} onChange={(lockAfterCreate) => updateState({ lockAfterCreate })} /></> : null}{error || status ? <p className="reminder-editor-status error" role={error ? "alert" : "status"}>{error || status}</p> : null}<div className="reminder-lock-actions"><button type="button" className="secondary" disabled={busyId !== undefined} onClick={() => updateState({ mode: "list", editing: undefined })}>Cancel</button><button type="submit" disabled={busyId !== undefined}>{state.mode === "edit" ? "Save changes" : "Create reminder"}</button></div></form>}
  </DialogShell>;
}
function ReminderCountdown({ nextDueAt }: { nextDueAt: string | number | undefined }) { const remaining = useCountdown(nextDueAt); return <small className="reminder-countdown">{formatCountdown(remaining, nextDueAt)}</small>; }
function formatCountdown(remaining: number, nextDueAt: string | number | undefined): string { if (nextDueAt === undefined || (typeof nextDueAt === "string" && !Number.isFinite(Date.parse(nextDueAt))) || (typeof nextDueAt === "number" && !Number.isFinite(nextDueAt))) return "Due time unavailable"; if (remaining === 0) return "Due now"; let seconds = Math.ceil(remaining / 1000); const days = Math.floor(seconds / 86_400); seconds -= days * 86_400; const hours = Math.floor(seconds / 3_600); seconds -= hours * 3_600; const minutes = Math.floor(seconds / 60); seconds -= minutes * 60; return `Due in ${[days ? `${days}d` : "", hours ? `${hours}h` : "", minutes ? `${minutes}m` : "", seconds ? `${seconds}s` : ""].filter(Boolean).join(" ")}`; }
function Toggle({ label, pressed, onChange }: { label: string; pressed: boolean; onChange: (value: boolean) => void }) { return <button type="button" className="goal-toggle" aria-pressed={pressed} onClick={() => onChange(!pressed)}><span className="goal-toggle-label">{label}</span><span className="goal-toggle-track" aria-hidden="true"><span className="goal-toggle-thumb"><span className="goal-toggle-state">{pressed ? "ON" : "OFF"}</span></span></span></button>; }
export { lockResultState };
