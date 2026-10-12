import { useEffect, useRef, useState } from "react";
import { modelOptionLabel } from "../domain/models";
import { sortPromptTemplatesByMRU, type PromptTemplate } from "../domain/prompt-templates";
import { DialogShell } from "./DialogShell";

export interface PromptControlsProps {
  templates: PromptTemplate[];
  loaded?: boolean;
  disabled?: boolean;
  onManage: () => void;
  onUse: (template: PromptTemplate) => void;
  usage?: Record<string, number>;
}

export function PromptControls({ templates, loaded = true, disabled = false, onManage, onUse, usage }: PromptControlsProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const buttonRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);
  useEffect(() => {
    if (!open) return;
    requestAnimationFrame(() => searchRef.current?.focus());
  }, [open]);

  const close = () => { setOpen(false); setQuery(""); buttonRef.current?.focus(); };

  const sortedTemplates = sortPromptTemplatesByMRU(templates, usage);
  const normalizedQuery = query.trim().toLowerCase();
  const visible = sortedTemplates.filter((template) =>
    !normalizedQuery ||
    template.name.toLowerCase().includes(normalizedQuery) ||
    template.body.toLowerCase().includes(normalizedQuery)
  );

  return (
    <div className="prompt-controls">
      <div className="prompt-picker-group">
        <span className="model-label">Prompt</span>
        <div className="model-picker">
          <button
            ref={buttonRef}
            type="button"
            className="model-current"
            aria-haspopup="dialog"
            aria-expanded={open}
            disabled={disabled}
            onClick={() => setOpen((value) => !value)}
          >
            Select prompt
            <i className="fa-solid fa-chevron-down" aria-hidden="true" />
          </button>
          {open ? (
            <DialogShell
              open={open}
              title="Choose prompt template"
              titleId="choose-prompt-template-title"
              onClose={close}
              closeLabel="Close prompt picker"
              className="message-dialog prompt-picker-dialog"
              returnFocus={buttonRef.current}
            >
              <div className="prompt-picker-body">
                <input
                  ref={searchRef}
                  className="model-search prompt-picker-search"
                  type="search"
                  aria-label="Search prompt templates"
                  placeholder="Search prompt templates…"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
                <div className="prompt-picker-scroll">
                  {!loaded ? (
                    <p className="prompt-picker-empty">Loading prompt templates…</p>
                  ) : visible.length === 0 ? (
                    <p className="prompt-picker-empty">No matching prompt templates</p>
                  ) : (
                    <div className="prompt-card-grid" role="listbox" aria-label="Prompt templates">
                      {visible.map((template) => (
                        <button
                          key={template.id ?? template.name}
                          type="button"
                          className="prompt-card model-option"
                          role="option"
                          aria-label={`Use prompt template ${template.name}`}
                          onClick={() => {
                            onUse(template);
                            close();
                          }}
                        >
                          <strong>{template.name}</strong>
                          <p>{template.body}</p>
                        </button>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            </DialogShell>
          ) : null}
        </div>
      </div>
      <button
        type="button"
        className="prompt-manage-button"
        aria-haspopup="dialog"
        aria-label="Manage prompt templates"
        title="Manage prompt templates"
        disabled={disabled}
        onClick={onManage}
      >
        <i className="fa-solid fa-gear" aria-hidden="true" />
      </button>
    </div>
  );
}

export { modelOptionLabel };

