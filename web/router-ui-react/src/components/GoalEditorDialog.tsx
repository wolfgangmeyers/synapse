import { useEffect, useState } from "react";
import { DEFAULT_GOAL_STATE, goalControlPayload, type GoalEditorValues, type GoalState } from "../domain/goals";
import { DialogShell } from "./DialogShell";

export interface GoalEditorDialogProps {
  open: boolean;
  peer: string;
  goalState?: GoalState;
  value?: GoalState;
  capabilityAvailable?: boolean;
  pending?: boolean;
  error?: string;
  returnFocus?: HTMLElement | null;
  onClose: () => void;
  onSave: (payload: ReturnType<typeof goalControlPayload>) => void;
}

function editorFromState(source: GoalState): GoalEditorValues {
  return {
    enabled: source.enabled,
    goal: source.goal,
    goal_lock_enabled: source.goal_lock_enabled,
    snooze_enabled: source.snooze_enabled,
    snooze_duration_seconds: source.snooze_duration_ms / 1000,
    snooze_turn_limit: source.snooze_turn_limit,
    snooze_budget_minutes: source.snooze_budget_cap_ms === null ? null : source.snooze_budget_cap_ms / 60_000,
    snooze_refill_yield_seconds: source.snooze_refill_yield_ms / 1000,
    snooze_refill_elapsed_seconds: source.snooze_refill_elapsed_ms / 1000,
  };
}

function Toggle({ label, pressed, lock, onChange }: { label: string; pressed: boolean; lock?: boolean; onChange: (value: boolean) => void }) {
  return <button type="button" className={`goal-toggle${lock ? " goal-lock-toggle" : ""}`} aria-pressed={pressed} aria-label={`${label}: ${lock ? (pressed ? "LOCKED" : "UNLOCKED") : (pressed ? "ON" : "OFF")}`} onClick={() => onChange(!pressed)}>
    <span className="goal-toggle-label">{label}</span><span className="goal-toggle-track" aria-hidden="true"><span className="goal-toggle-thumb"><span className="goal-toggle-state">{lock ? (pressed ? "LOCKED" : "UNLOCKED") : (pressed ? "ON" : "OFF")}</span></span></span>
  </button>;
}

export function GoalEditorDialog({ open, peer, goalState, value, capabilityAvailable = true, pending = false, error, returnFocus, onClose, onSave }: GoalEditorDialogProps) {
  const [draft, setDraft] = useState<GoalEditorValues>(editorFromState(value ?? goalState ?? DEFAULT_GOAL_STATE));
  const [validationError, setValidationError] = useState("");
  const source = value ?? goalState;
  const sourceKey = JSON.stringify(editorFromState(source ?? DEFAULT_GOAL_STATE));
  useEffect(() => { if (open) { setDraft(editorFromState(source ?? DEFAULT_GOAL_STATE)); setValidationError(""); } }, [open, peer, sourceKey]);
  const set = <K extends keyof GoalEditorValues>(key: K, next: GoalEditorValues[K]) => setDraft((current) => ({ ...current, [key]: next }));
  const save = () => {
    const positive = (item: number): boolean => Number.isSafeInteger(item) && item > 0;
    const optional = (item: number | null): boolean => item === null || positive(item);
    if (draft.enabled && draft.goal.trim() === "") { setValidationError("Turn on the goal and enter a description before saving."); return; }
    if (!positive(draft.snooze_duration_seconds) || !optional(draft.snooze_turn_limit) || (draft.snooze_turn_limit !== null && draft.snooze_turn_limit > 20) || !optional(draft.snooze_budget_minutes) || !positive(draft.snooze_refill_yield_seconds) || !positive(draft.snooze_refill_elapsed_seconds)) { setValidationError("Check the goal settings and try again."); return; }
    setValidationError(""); onSave(goalControlPayload(draft));
  };
  const goalError = validationError || error;
  const dialogProps = { open, title: `Goal for ${peer}`, titleId: "goal-dialog-title", description: "Set what this agent should work toward.", onClose, closeLabel: "Close goal editor", className: "message-dialog goal-editor-dialog", ...(returnFocus === undefined ? {} : { returnFocus }) };
  return <DialogShell {...dialogProps}>
    {!capabilityAvailable ? <p className="error" role="alert">Goal controls are not available on this router.</p> : <div className="goal-editor-form">
      <Toggle label="Goal" pressed={draft.enabled} onChange={(next) => set("enabled", next)} />
      <label>What should this agent work toward?<textarea id="goal-text" maxLength={4000} rows={10} placeholder="Describe the goal in Markdown" value={draft.goal} disabled={!draft.enabled} onChange={(event) => { set("goal", event.target.value); setValidationError(""); }} /></label>
      <Toggle label="Goal lock" lock pressed={draft.goal_lock_enabled} onChange={(next) => set("goal_lock_enabled", next)} />
      <fieldset><legend>Snooze settings</legend>
        <Toggle label="Allow snoozing" pressed={draft.snooze_enabled} onChange={(next) => set("snooze_enabled", next)} />
        <NumberField label="Snooze duration (seconds)" value={draft.snooze_duration_seconds} min={1} onChange={(next) => { if (next !== "") set("snooze_duration_seconds", next); }} />
        <NumberField label="Snooze turn limit (optional)" value={draft.snooze_turn_limit ?? ""} min={1} max={20} onChange={(next) => set("snooze_turn_limit", next === "" ? null : next)} />
        <NumberField label="Snooze budget (minutes, optional)" value={draft.snooze_budget_minutes ?? ""} min={1} onChange={(next) => set("snooze_budget_minutes", next === "" ? null : next)} />
        <NumberField label="Snooze refill yield (seconds)" value={draft.snooze_refill_yield_seconds} min={1} onChange={(next) => { if (next !== "") set("snooze_refill_yield_seconds", next); }} />
        <NumberField label="Snooze refill time (seconds)" value={draft.snooze_refill_elapsed_seconds} min={1} onChange={(next) => { if (next !== "") set("snooze_refill_elapsed_seconds", next); }} />
      </fieldset>
      <p className="goal-outcome-counts">Outcomes: completed {source?.outcome_counts.COMPLETED ?? 0}, not done {source?.outcome_counts.NOT_DONE ?? 0}</p>
      {goalError ? <p className="goal-editor-status error" role="alert">{goalError}</p> : null}
      <div className="goal-editor-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="button" disabled={pending || !capabilityAvailable} onClick={save}>{pending ? "Saving…" : "Save"}</button></div>
    </div>}
  </DialogShell>;
}

function NumberField({ label, value, min, max, onChange }: { label: string; value: number | ""; min: number; max?: number; onChange: (value: number | "") => void }) {
  return <label className="goal-number-field">{label}<input type="number" min={min} max={max} step={1} value={value} onChange={(event) => { const text = event.target.value; onChange(text === "" ? "" : Number(text)); }} /></label>;
}
