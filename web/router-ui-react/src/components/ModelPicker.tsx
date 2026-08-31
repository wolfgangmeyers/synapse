import { useEffect, useId, useMemo, useRef, useState } from "react";
import { modelKey, modelOptionLabel, normalizeModelSummaries, type ModelSummary } from "../domain/models";

export interface ModelPickerProps {
  models: ModelSummary[];
  currentModel?: ModelSummary;
  totalCount?: number;
  truncated?: boolean;
  pending?: boolean;
  disabled?: boolean;
  onChange: (model: ModelSummary) => void;
  label?: string;
}

export function ModelPicker({ models, currentModel, totalCount = models.length, truncated = false, pending = false, disabled = false, onChange, label = "Model" }: ModelPickerProps) {
  const normalized = useMemo(() => normalizeModelSummaries(models), [models]);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [optimisticModel, setOptimisticModel] = useState<ModelSummary | undefined>();
  const displayedModel = optimisticModel ?? currentModel;
  useEffect(() => {
    if (!pending || (optimisticModel && ((currentModel && modelKey(optimisticModel) === modelKey(currentModel)) || !normalized.some((model) => modelKey(model) === modelKey(optimisticModel))))) setOptimisticModel(undefined);
  }, [currentModel, normalized, optimisticModel, pending]);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listId = useId();
  const visible = normalized.filter((model) => `${modelKey(model)} ${modelOptionLabel(model)}`.toLowerCase().includes(query.trim().toLowerCase()));

  useEffect(() => {
    if (!open) return;
    requestAnimationFrame(() => searchRef.current?.focus());
  }, [open]);
  useEffect(() => {
    if (!disabled) return;
    setOpen(false); setQuery("");
  }, [disabled]);

  const close = () => { setOpen(false); setQuery(""); buttonRef.current?.focus(); };
  const choose = (model: ModelSummary) => { setOptimisticModel(model); onChange(model); close(); };
  return <div className="model-controls">
    <span className="model-label">{label}</span>
    <div className="model-picker">
      <button ref={buttonRef} type="button" className="model-current" aria-haspopup="listbox" aria-expanded={open} aria-controls={listId} disabled={disabled} onClick={() => setOpen((value) => !value)}>{displayedModel ? modelKey(displayedModel) : "Select model"}<i className="fa-solid fa-chevron-down" aria-hidden="true" /></button>
      {open ? <div id={listId} className="model-menu" role="dialog" aria-label="Choose agent model">
        <input ref={searchRef} className="model-search" type="search" aria-label="Search available models" placeholder="Search models…" value={query} onChange={(event) => setQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Escape") close(); }} />
        {truncated ? <p className="model-catalog-note">Showing {normalized.length} of {Math.max(normalized.length, totalCount)} models.</p> : null}
        <div className="model-options" role="listbox" aria-busy={pending}>
          {visible.length ? visible.map((model) => <button key={modelKey(model)} type="button" className="model-option" role="option" aria-selected={modelKey(model) === (displayedModel ? modelKey(displayedModel) : "")} disabled={pending} onClick={() => choose(model)}><strong>{modelOptionLabel(model)}</strong><small>{modelKey(model)}</small></button>) : <p className="model-options-empty">No matching models</p>}
        </div>
      </div> : null}
    </div>
  </div>;
}
