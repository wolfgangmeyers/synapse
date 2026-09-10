import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { modelKey, modelOptionLabel, normalizeModelSummaries, type ModelSummary } from "../domain/models";

export interface ModelPickerEmptyOption {
  label: string;
  description?: string;
  onSelect(): void;
}

export interface ModelPickerProps {
  models: ModelSummary[];
  currentModel?: ModelSummary;
  totalCount?: number;
  truncated?: boolean;
  pending?: boolean;
  disabled?: boolean;
  loading?: boolean;
  emptyMessage?: string;
  emptyOption?: ModelPickerEmptyOption;
  onChange: (model: ModelSummary) => void;
  label?: string;
}

export function ModelPicker({ models, currentModel, totalCount = models.length, truncated = false, pending = false, disabled = false, loading = false, emptyMessage = "No matching models", emptyOption, onChange, label = "Model" }: ModelPickerProps) {
  const normalized = useMemo(() => normalizeModelSummaries(models), [models]);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [optimisticModel, setOptimisticModel] = useState<ModelSummary | undefined>();
  const [menuHost, setMenuHost] = useState<HTMLElement>();
  const [menuPosition, setMenuPosition] = useState<CSSProperties>();
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
  useLayoutEffect(() => {
    if (!open || !buttonRef.current) { setMenuHost(undefined); setMenuPosition(undefined); return; }
    const trigger = buttonRef.current;
    const dialog = trigger.closest("dialog");
    setMenuHost(dialog ?? document.body);
    const positionMenu = () => {
      const rect = trigger.getBoundingClientRect();
      const width = Math.min(360, Math.max(0, window.innerWidth - 32));
      const left = Math.min(Math.max(16, rect.right - width), Math.max(16, window.innerWidth - width - 16));
      const spaceBelow = window.innerHeight - rect.bottom - 16;
      const spaceAbove = rect.top - 16;
      const above = spaceBelow < 280 && spaceAbove > spaceBelow;
      const dialogRect = dialog?.getBoundingClientRect();
      // A native dialog is in the top layer. Keep its menu there, but position it
      // against the dialog rather than treating its fixed containing block as the viewport.
      setMenuPosition({ position: dialogRect ? "absolute" : "fixed", zIndex: 100, left: dialogRect ? left - dialogRect.left : left, right: "auto", width, ...(above ? { top: "auto", bottom: dialogRect ? dialogRect.bottom - rect.top + 8 : window.innerHeight - rect.top + 8 } : { top: dialogRect ? rect.bottom - dialogRect.top + 8 : rect.bottom + 8, bottom: "auto" }), maxHeight: Math.max(120, Math.min(440, (above ? spaceAbove : spaceBelow) - 8)) });
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
  }, [open]);

  const close = () => { setOpen(false); setQuery(""); buttonRef.current?.focus(); };
  const choose = (model: ModelSummary) => { setOptimisticModel(model); onChange(model); close(); };
  const chooseEmpty = () => { setOptimisticModel(undefined); emptyOption?.onSelect(); close(); };
  const menu = open && menuHost ? <div id={listId} className="model-menu model-menu-portal" style={menuPosition} role="dialog" aria-label="Choose agent model" onKeyDown={(event) => { if (event.key === "Escape") close(); }}>
    <input ref={searchRef} className="model-search" type="search" aria-label="Search available models" placeholder="Search models…" value={query} onChange={(event) => setQuery(event.target.value)} />
    {truncated ? <p className="model-catalog-note">Showing {normalized.length} of {Math.max(normalized.length, totalCount)} models.</p> : null}
    <div className="model-options" role="listbox" aria-busy={pending || loading}>
      {emptyOption ? <button type="button" className="model-option" role="option" aria-selected={displayedModel === undefined} disabled={pending} onClick={chooseEmpty}><strong>{emptyOption.label}</strong>{emptyOption.description ? <small>{emptyOption.description}</small> : null}</button> : null}
      {loading ? <p className="model-options-empty">Loading available models…</p> : visible.length ? visible.map((model) => <button key={modelKey(model)} type="button" className="model-option" role="option" aria-selected={modelKey(model) === (displayedModel ? modelKey(displayedModel) : "")} disabled={pending} onClick={() => choose(model)}><strong>{modelOptionLabel(model)}</strong><small>{modelKey(model)}</small></button>) : <p className="model-options-empty">{emptyMessage}</p>}
    </div>
  </div> : null;
  return <div className="model-controls">
    <span className="model-label">{label}</span>
    <div className="model-picker" onKeyDown={(event) => { if (event.key === "Escape" && open) close(); }}>
      <button ref={buttonRef} type="button" className="model-current" aria-haspopup="dialog" aria-expanded={open} aria-controls={listId} disabled={disabled} onClick={() => setOpen((value) => !value)}>{displayedModel ? modelKey(displayedModel) : emptyOption?.label ?? "Select model"}<i className="fa-solid fa-chevron-down" aria-hidden="true" /></button>
    </div>
    {menu && menuHost ? createPortal(menu, menuHost) : null}
  </div>;
}
