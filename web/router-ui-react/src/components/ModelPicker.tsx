import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { createPortal } from "react-dom";
import { isClaudeCodeModel, isAntigravityModel, sameModelEngine, modelKey, modelOptionLabel, normalizeModelSummaries, type ModelSummary } from "../domain/models";

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
  status?: string;
  onChange: (model: ModelSummary) => void;
  label?: string;
  isEmbedded?: boolean;
}

const ADMITTED_EMBEDDED_PROVIDERS = new Set(['openai-codex', 'google', 'cursor', 'google-vertex']);

function isAdmittedEmbeddedProvider(provider: string): boolean {
  return ADMITTED_EMBEDDED_PROVIDERS.has(provider) || provider.startsWith("custom-");
}

export function ModelPicker({ models, currentModel, totalCount = models.length, truncated = false, pending = false, disabled = false, loading = false, emptyMessage = "No matching models", emptyOption, status, onChange, label = "Model", isEmbedded }: ModelPickerProps) {
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
  const pool = isEmbedded ? normalized.filter(m => sameModelEngine(currentModel, m) && (isClaudeCodeModel(m) || isAntigravityModel(m) || isAdmittedEmbeddedProvider(m.provider))) : normalized;
  const visible = pool.filter(model => `${modelKey(model)} ${modelOptionLabel(model)}`.toLowerCase().includes(query.trim().toLowerCase()));

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
      const dialogRect = dialog?.getBoundingClientRect();
      const leftBound = Math.max(16, dialogRect && dialog ? dialogRect.left + dialog.clientLeft : 16);
      const rightBound = Math.min(window.innerWidth - 16, dialogRect && dialog ? dialogRect.left + dialog.clientLeft + dialog.clientWidth : window.innerWidth - 16);
      const topBound = Math.max(16, dialogRect && dialog ? dialogRect.top + dialog.clientTop : 16);
      const bottomBound = Math.min(window.innerHeight - 16, dialogRect && dialog ? dialogRect.top + dialog.clientTop + dialog.clientHeight : window.innerHeight - 16);
      if (rightBound <= leftBound || bottomBound <= topBound || rect.bottom <= topBound || rect.top >= bottomBound || rect.right <= leftBound || rect.left >= rightBound) {
        setOpen(false); setQuery(""); return;
      }
      const width = Math.min(360, Math.max(0, rightBound - leftBound));
      const left = Math.max(leftBound, Math.min(rect.right - width, rightBound - width));
      const spaceBelow = Math.max(0, bottomBound - rect.bottom - 8);
      const spaceAbove = Math.max(0, rect.top - topBound - 8);
      const above = spaceBelow < 280 && spaceAbove > spaceBelow;
      const available = above ? spaceAbove : spaceBelow;
      // When neither side has useful room, overlap the trigger instead of collapsing.
      const overlap = available < 160;
      const height = Math.min(440, overlap ? Math.max(0, bottomBound - topBound) : available);
      const top = overlap ? topBound : above ? rect.top - 8 : rect.bottom + 8;
      // Absolute coordinates are relative to the dialog padding edge and its scroll offset.
      setMenuPosition({ position: dialogRect ? "absolute" : "fixed", zIndex: 100,
        left: dialogRect && dialog ? left - dialogRect.left - dialog.clientLeft + dialog.scrollLeft : left,
        top: dialogRect && dialog ? top - dialogRect.top - dialog.clientTop + dialog.scrollTop : top,
        right: "auto", bottom: "auto", width, maxHeight: height, transform: above && !overlap ? "translateY(-100%)" : undefined });
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
    <span className="model-label">
      <i className="fa-solid fa-cube" aria-hidden="true" style={{ marginRight: 6 }} />
      {label}
    </span>
    <div className="model-picker" onKeyDown={(event) => { if (event.key === "Escape" && open) close(); }}>
      <button ref={buttonRef} type="button" className="model-current" aria-haspopup="dialog" aria-expanded={open} aria-controls={listId} disabled={disabled} onClick={() => setOpen((value) => !value)}>{displayedModel ? modelKey(displayedModel) : emptyOption?.label ?? "Select model"}<i className="fa-solid fa-chevron-down" aria-hidden="true" /></button>
    </div>
    {menu && menuHost ? createPortal(menu, menuHost) : null}
    {status ? <p className="model-status" role="alert">{status}</p> : null}
  </div>;
}
