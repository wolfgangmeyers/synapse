import { useEffect, useRef, useState } from "react";
import { activeRemindersForPeer, createReminderPayload, lockResultState, reminderLockKey, reminderRecurrenceLabel, validateReminderDelay, type Reminder, type ReminderCreateInput, type ReminderLockAction } from "../domain/reminders";
import { DialogShell } from "./DialogShell";
import { useCountdown } from "../hooks/use-countdown";

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
}

type Mode = "list" | "create" | "edit";
function lockFor(locks: ReminderDialogProps["locks"], key: string): boolean { return locks instanceof Map ? locks.get(key) === true : locks?.[key] === true; }

export function ReminderDialog({ open, peer, creator, reminders, locks, pendingIds = new Set<string>(), status, onClose, onCreate, onUpdate, onDelete, onLock }: ReminderDialogProps) {
  const [mode, setMode] = useState<Mode>("list");
  const [editing, setEditing] = useState<Reminder | undefined>();
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [delay, setDelay] = useState(1);
  const [recurring, setRecurring] = useState(false);
  const [lockAfterCreate, setLockAfterCreate] = useState(false);
  const [error, setError] = useState("");
  const [busyId, setBusyId] = useState<string>();
  const previousPeer = useRef(peer);
  useEffect(() => {
    if (!open || previousPeer.current !== peer) {
      setMode("list"); setEditing(undefined); setSubject(""); setBody(""); setDelay(1); setRecurring(false); setLockAfterCreate(false); setError(""); setBusyId(undefined);
    }
    previousPeer.current = peer;
  }, [open, peer]);
  const active = activeRemindersForPeer(reminders, peer);
  const startCreate = () => { setMode("create"); setEditing(undefined); setSubject(""); setBody(""); setDelay(1); setRecurring(false); setLockAfterCreate(false); setError(""); };
  const startEdit = (reminder: Reminder) => { setMode("edit"); setEditing(reminder); setSubject(reminder.subject ?? ""); setBody(reminder.body); setError(""); };
  const submit = async (event: React.FormEvent<HTMLFormElement>): Promise<void> => {
    event.preventDefault(); const trimmed = body.trim();
    if (!trimmed) { setError("Enter reminder text."); return; }
    if (editing) {
      setBusyId(editing.id); setError("");
      try { await onUpdate(editing.id, { subject: subject.trim() || null, body: trimmed }); setMode("list"); setEditing(undefined); }
      catch (cause) { setError(cause instanceof Error ? cause.message : "Reminder could not be updated."); }
      finally { setBusyId(undefined); }
      return;
    }
    if (!validateReminderDelay(delay)) { setError("Enter a delay from 1 to 10080 minutes."); return; }
    const options = { recurring, ...(subject.trim() ? { subject: subject.trim() } : {}) };
    try { await onCreate(createReminderPayload(peer, creator, trimmed, delay, options), lockAfterCreate); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "Reminder could not be created."); }
  };
  const title = mode === "list" ? "Reminders" : mode === "edit" ? "Edit reminder" : "New reminder";
  return <DialogShell open={open} title={title} titleId="reminders-dialog-title" description="Scheduled reminders for this agent." onClose={onClose} closeLabel="Close reminders" className="message-dialog">
    {mode === "list" ? <div className="reminders-dialog-body"><button type="button" onClick={startCreate}>Add reminder</button>{error || status ? <p className="reminder-editor-status error" role={error ? "alert" : "status"}>{error || status}</p> : null}{active.length === 0 ? <p className="empty-state">No active reminders.</p> : active.map((reminder) => {
      const key = reminderLockKey(peer, reminder.id); const locked = lockFor(locks, key); const pending = pendingIds.has(reminder.id) || busyId === reminder.id;
      const remove = async (): Promise<void> => { setBusyId(reminder.id); setError(""); try { await onDelete(reminder.id); } catch (cause) { setError(cause instanceof Error ? cause.message : "Reminder could not be deleted."); } finally { setBusyId(undefined); } };
      return <article className="reminder-list-item" key={reminder.id}><strong>{reminder.subject || "Reminder"}</strong><p>{reminder.body}</p><ReminderCountdown nextDueAt={reminder.next_due_at} /><small className="reminder-recurrence">{reminderRecurrenceLabel(reminder.interval_ms)}</small><small className="reminder-lock-status">{locked ? "Native edits blocked." : ""}</small><div className="reminder-lock-actions"><button type="button" className="secondary" disabled={pending} onClick={() => startEdit(reminder)}>Edit</button><button type="button" className="secondary" disabled={pending} onClick={() => { void remove(); }}>Delete</button><button type="button" className="secondary" aria-pressed={locked} disabled={pending} onClick={() => onLock(reminder, locked ? "clear" : "set")}>{locked ? "Unlock" : "Lock"}</button></div></article>;
    })}</div> : <form className="reminder-editor-form" noValidate onSubmit={(event) => { void submit(event); }}><label>Subject<input value={subject} placeholder="Optional" onChange={(event) => setSubject(event.target.value)} /></label><label>Reminder<textarea value={body} placeholder="What should be sent?" rows={5} onChange={(event) => { setBody(event.target.value); setError(""); }} /></label>{mode === "create" ? <><label>Send after (minutes)<input type="number" min={1} max={10080} step={1} value={delay} onChange={(event) => setDelay(Number(event.target.value))} /></label><Toggle label="Repeat every delay interval" pressed={recurring} onChange={setRecurring} /><Toggle label="Reminder lock" pressed={lockAfterCreate} onChange={setLockAfterCreate} /></> : null}{error || status ? <p className="reminder-editor-status error" role={error ? "alert" : "status"}>{error || status}</p> : null}<div className="reminder-lock-actions"><button type="button" className="secondary" disabled={busyId !== undefined} onClick={() => setMode("list")}>Cancel</button><button type="submit" disabled={busyId !== undefined}>{mode === "edit" ? "Save changes" : "Create reminder"}</button></div></form>}
  </DialogShell>;
}
function ReminderCountdown({ nextDueAt }: { nextDueAt: string | number | undefined }) { const remaining = useCountdown(nextDueAt); return <small className="reminder-countdown">{formatCountdown(remaining, nextDueAt)}</small>; }
function formatCountdown(remaining: number, nextDueAt: string | number | undefined): string { if (nextDueAt === undefined || (typeof nextDueAt === "string" && !Number.isFinite(Date.parse(nextDueAt))) || (typeof nextDueAt === "number" && !Number.isFinite(nextDueAt))) return "Due time unavailable"; if (remaining === 0) return "Due now"; let seconds = Math.ceil(remaining / 1000); const days = Math.floor(seconds / 86_400); seconds -= days * 86_400; const hours = Math.floor(seconds / 3_600); seconds -= hours * 3_600; const minutes = Math.floor(seconds / 60); seconds -= minutes * 60; return `Due in ${[days ? `${days}d` : "", hours ? `${hours}h` : "", minutes ? `${minutes}m` : "", seconds ? `${seconds}s` : ""].filter(Boolean).join(" ")}`; }
function Toggle({ label, pressed, onChange }: { label: string; pressed: boolean; onChange: (value: boolean) => void }) { return <button type="button" className="goal-toggle" aria-pressed={pressed} onClick={() => onChange(!pressed)}><span className="goal-toggle-label">{label}</span><span className="goal-toggle-track" aria-hidden="true"><span className="goal-toggle-thumb"><span className="goal-toggle-state">{pressed ? "ON" : "OFF"}</span></span></span></button>; }
export { lockResultState };
