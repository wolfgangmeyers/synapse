import { useEffect, useRef, useState } from "react";
import { modelOptionLabel } from "../domain/models";
import { type PromptTemplate } from "../domain/prompt-templates";

export interface PromptControlsProps {
  templates: PromptTemplate[];
  loaded?: boolean;
  disabled?: boolean;
  onManage: () => void;
  onUse: (template: PromptTemplate) => void;
}

export function PromptControls({ templates, loaded = true, disabled = false, onManage, onUse }: PromptControlsProps) {
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);
  const close = () => { setOpen(false); buttonRef.current?.focus(); };
  return <div className="prompt-controls">
    <div className="prompt-picker-group"><span className="model-label">Prompt</span><div className="model-picker">
      <button ref={buttonRef} type="button" className="model-current" aria-haspopup="listbox" aria-expanded={open} disabled={disabled} onClick={() => setOpen((value) => !value)}>Select prompt<i className="fa-solid fa-chevron-down" aria-hidden="true" /></button>
      {open ? <div className="model-menu" role="dialog" aria-label="Choose prompt template"><div className="model-options" role="listbox">{!loaded ? <p className="model-options-empty">Loading prompt templates…</p> : templates.length === 0 ? <p className="model-options-empty">No saved prompt templates</p> : templates.slice(0, 10).map((template) => <button key={template.id ?? template.name} type="button" className="model-option" role="option" aria-label={`Use prompt template ${template.name}`} onClick={() => { onUse(template); close(); }}><strong>{template.name}</strong><small>{template.body}</small></button>)}</div></div> : null}
    </div></div><button type="button" className="prompt-manage-button" aria-haspopup="dialog" aria-label="Manage prompt templates" title="Manage prompt templates" disabled={disabled} onClick={onManage}><i className="fa-solid fa-gear" aria-hidden="true" /></button>
  </div>;
}

export { modelOptionLabel };
