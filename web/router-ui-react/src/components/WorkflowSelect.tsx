import { useState, useMemo, useRef, useId, useLayoutEffect, useEffect, type CSSProperties, type ReactElement } from "react";
import { createPortal } from "react-dom";
import type { Workflow } from "../domain/workflow-states";

export interface WorkflowSelectProps {
  workflows: Workflow[];
  value: string;
  onChange: (workflowId: string) => void;
  placeholder?: string;
  disabled?: boolean;
}

export function WorkflowSelect({
  workflows,
  value,
  onChange,
  placeholder = "Select a workflow...",
  disabled = false,
}: WorkflowSelectProps): ReactElement {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [menuHost, setMenuHost] = useState<HTMLElement>();
  const [menuPosition, setMenuPosition] = useState<CSSProperties>();

  const buttonRef = useRef<HTMLButtonElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const listId = useId();

  const selectedWorkflow = workflows.find((w) => w.id === value);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return workflows;
    return workflows.filter(
      (w) =>
        w.name.toLowerCase().includes(q) ||
        w.id.toLowerCase().includes(q) ||
        (w.description && w.description.toLowerCase().includes(q))
    );
  }, [workflows, query]);

  useEffect(() => {
    if (!open) return;
    requestAnimationFrame(() => searchRef.current?.focus());
  }, [open]);

  useEffect(() => {
    if (!disabled) return;
    setOpen(false);
    setQuery("");
  }, [disabled]);

  useLayoutEffect(() => {
    if (!open || !buttonRef.current) {
      setMenuHost(undefined);
      setMenuPosition(undefined);
      return;
    }
    const trigger = buttonRef.current;
    const dialog = trigger.closest("dialog");
    setMenuHost(dialog ?? document.body);

    const positionMenu = () => {
      const rect = trigger.getBoundingClientRect();
      const dialogRect = dialog?.getBoundingClientRect();

      const width = rect.width;
      const left = dialogRect ? rect.left - dialogRect.left : rect.left;
      const spaceBelow = window.innerHeight - rect.bottom - 16;
      const spaceAbove = rect.top - 16;
      const above = spaceBelow < 240 && spaceAbove > spaceBelow;

      setMenuPosition({
        position: dialogRect ? "absolute" : "fixed",
        zIndex: 1000,
        left,
        right: "auto",
        width,
        ...(above
          ? {
              top: "auto",
              bottom: dialogRect ? dialogRect.bottom - rect.top + 8 : window.innerHeight - rect.top + 8,
            }
          : {
              top: dialogRect ? rect.bottom - dialogRect.top + 8 : rect.bottom + 8,
              bottom: "auto",
            }),
        maxHeight: Math.max(120, Math.min(260, (above ? spaceAbove : spaceBelow) - 8)),
      });
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

  const close = () => {
    setOpen(false);
    setQuery("");
    buttonRef.current?.focus();
  };

  const choose = (wf: Workflow) => {
    onChange(wf.id);
    close();
  };

  const menu = open && menuHost ? (
    <div
      id={listId}
      className="model-menu model-menu-portal"
      style={menuPosition}
      role="dialog"
      aria-label="Choose workflow"
      onKeyDown={(event) => {
        if (event.key === "Escape") close();
      }}
    >
      <input
        ref={searchRef}
        className="model-search"
        type="search"
        aria-label="Search workflows"
        placeholder="Search workflows…"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      <div className="model-options" role="listbox">
        {filtered.length ? (
          filtered.map((w) => (
            <button
              key={w.id}
              type="button"
              className="model-option"
              role="option"
              aria-selected={w.id === value}
              onClick={() => choose(w)}
            >
              <strong>{w.name}</strong>
              {w.description ? <small>{w.description}</small> : null}
            </button>
          ))
        ) : (
          <p className="model-options-empty">No matching workflows</p>
        )}
      </div>
    </div>
  ) : null;

  return (
    <div className="model-picker" onKeyDown={(event) => { if (event.key === "Escape" && open) close(); }}>
      <button
        ref={buttonRef}
        type="button"
        className="model-current"
        style={{ width: "100%", justifyContent: "space-between", height: "40px", padding: "0 12px" }}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={listId}
        disabled={disabled || workflows.length === 0}
        onClick={() => setOpen((prev) => !prev)}
      >
        <span style={{ color: selectedWorkflow ? "var(--text, #f8fafc)" : "var(--muted, #94a3b8)" }}>
          {selectedWorkflow ? selectedWorkflow.name : placeholder}
        </span>
        <i className="fa-solid fa-chevron-down" aria-hidden="true" />
      </button>
      {menu && menuHost ? createPortal(menu, menuHost) : null}
    </div>
  );
}
