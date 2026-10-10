import { useEffect, useState } from "react";
import { normalizeRouterUrl, routerDisplayName, type SavedRouter } from "../domain/router-manager";
import { DEFAULT_AGENT_ID } from "../persistence/settings-repository";
import { DialogShell } from "./DialogShell";

export interface RouterManagerDialogProps {
  open: boolean;
  routers: SavedRouter[];
  activeUrl: string;
  defaultAgentId?: string;
  onClose: () => void;
  onSave: (router: SavedRouter, previousUrl?: string) => void;
  onSelect: (router: SavedRouter) => void;
  onRemove: (url: string) => void;
  onProviderSettings?: () => void;
}

type Editor = { mode: "add" | "edit"; previousUrl?: string };

export function RouterManagerDialog({ open, routers, activeUrl, defaultAgentId = DEFAULT_AGENT_ID, onClose, onSave, onSelect, onRemove, onProviderSettings = () => undefined }: RouterManagerDialogProps) {
  const [editor, setEditor] = useState<Editor | null>(null);
  const [url, setUrl] = useState("");
  const [alias, setAlias] = useState("");
  const [agentId, setAgentId] = useState(defaultAgentId);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) setEditor(null);
  }, [open]);

  const beginAdd = () => {
    setEditor({ mode: "add" }); setUrl(activeUrl); setAlias(""); setAgentId(defaultAgentId); setError("");
  };
  const beginEdit = (router: SavedRouter) => {
    setEditor({ mode: "edit", previousUrl: router.url }); setUrl(router.url); setAlias(router.alias ?? ""); setAgentId(router.agentId); setError("");
  };
  const save = () => {
    try {
      const canonical = normalizeRouterUrl(url);
      const saved: SavedRouter = { url: canonical, ...(alias.trim() ? { alias: alias.trim() } : {}), agentId: agentId.trim() || defaultAgentId };
      if (editor?.previousUrl === undefined) onSave(saved);
      else onSave(saved, editor.previousUrl);
      setEditor(null); setError("");
    } catch (value) {
      setError(value instanceof Error ? value.message : "Enter a valid router URL.");
    }
  };
  const remove = (router: SavedRouter) => {
    if (routers.length <= 1) return;
    onRemove(router.url);
  };

  return (
    <DialogShell open={open} title="Routers" titleId="router-dialog-title" description="Each router keeps its own agents and chat history." onClose={onClose} closeLabel="Close routers" className="message-dialog router-dialog">
      <div className="dialog-actions router-dialog-actions"><button type="button" className="secondary" onClick={onProviderSettings}>Provider settings</button><button type="button" aria-label="Add router" onClick={beginAdd}>Add router</button></div>
      <div className="saved-routers">
        {routers.map((router) => <div key={router.url} className={`router-row${router.url === activeUrl ? " active" : ""}`} tabIndex={0} role="button" aria-current={router.url === activeUrl ? "true" : undefined} onClick={() => onSelect(router)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelect(router); } }}>
          <div className="router-label"><strong>{routerDisplayName(router.url, router.alias)}</strong><div className="meta">{router.alias ? `${router.url}${router.url === activeUrl ? " · selected" : ""}` : router.url === activeUrl ? "selected" : ""}</div></div>
          <div className="router-row-actions">
            <button type="button" className="icon-button secondary" aria-label={`Edit ${routerDisplayName(router.url)}`} onClick={(event) => { event.stopPropagation(); beginEdit(router); }}>✎</button>
            <button type="button" className="icon-button danger" aria-label={`Remove ${routerDisplayName(router.url)}`} disabled={routers.length === 1} onClick={(event) => { event.stopPropagation(); remove(router); }}>×</button>
          </div>
        </div>)}
      </div>
      {editor ? <section className="router-setup"><h3>{editor.mode === "edit" ? "Edit router" : "Add router"}</h3>
        <label>Hostname or URL<input value={url} onChange={(event) => setUrl(event.target.value)} placeholder="http://127.0.0.1:8765" autoFocus /></label>
        <label>Router name<input value={alias} onChange={(event) => setAlias(event.target.value)} placeholder="Home lab" /></label>
        <label>Connect as user<input value={agentId} onChange={(event) => setAgentId(event.target.value)} spellCheck={false} /></label>
        {error ? <p className="error" role="alert">{error}</p> : null}
        <div className="router-form-actions"><button type="button" className="secondary" onClick={() => { setEditor(null); setError(""); }}>Cancel</button><button type="button" onClick={save}>{editor.mode === "edit" ? "Save router" : "Add router"}</button></div>
      </section> : null}
    </DialogShell>
  );
}

