import { useEffect, useState, type FormEvent, type ReactElement } from "react";
import type { ManagedAgent, ModelSummary } from "../protocol/types";
import type { ModelsResponse } from "../transport/http-client";
import { DialogShell } from "./DialogShell";
import { ManagedDirectoryPicker } from "./ManagedDirectoryPicker";
import { ModelPicker } from "./ModelPicker";

export function normalizeManagedAgentName(name: string): string { return name.trim().replace(/\s+/g, "-"); }

export interface ManagedAgentDialogProps {
  open: boolean;
  agent?: ManagedAgent;
  pending?: boolean;
  error?: string;
  recentFolders?: readonly string[];
  onClose(): void;
  onLaunch(input: { name: string; cwd: string; runtime?: "rpc" | "embedded"; model?: { provider: string; id: string }; initial_instruction?: string }): Promise<void>;
  onAction(agentId: string, action: "restart" | "reset" | "terminate"): Promise<void>;
  onListDirectories(path: string): Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
  onModels?(): Promise<ModelsResponse>;
}

export function ManagedAgentDialog({ open, agent, pending = false, error, recentFolders, onClose, onLaunch, onAction, onListDirectories, onModels }: ManagedAgentDialogProps): ReactElement {
  const [name, setName] = useState("");
  const [cwd, setCwd] = useState("");
  const [selectedModel, setSelectedModel] = useState<ModelSummary>();
  const [catalog, setCatalog] = useState<ModelsResponse>();
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [catalogError, setCatalogError] = useState<string>();
  const [instruction, setInstruction] = useState("");
  const [confirmTermination, setConfirmTermination] = useState(false);
  useEffect(() => { if (!open) return; setName(""); setCwd(agent?.cwd ?? ""); setSelectedModel(agent?.model); setInstruction(""); setConfirmTermination(false); }, [agent, open]);
  useEffect(() => {
    let cancelled = false;
    if (!open || agent || !onModels) { setCatalog(undefined); setCatalogLoading(false); setCatalogError(undefined); return () => { cancelled = true; }; }
    setCatalogLoading(true); setCatalogError(undefined);
    void onModels().then((result) => { if (!cancelled) setCatalog(result); }).catch(() => { if (!cancelled) setCatalogError("Available models could not be loaded. You can still use the router default."); }).finally(() => { if (!cancelled) setCatalogLoading(false); });
    return () => { cancelled = true; };
  }, [agent, onModels, open]);
  const submit = (event: FormEvent<HTMLFormElement>): void => {
    event.preventDefault();
    void onLaunch({ name: normalizeManagedAgentName(name), cwd: cwd.trim(), runtime: "embedded", ...(selectedModel ? { model: { provider: selectedModel.provider, id: selectedModel.id } } : {}), ...(instruction.trim() ? { initial_instruction: instruction } : {}) });
  };
  const availableModels = (catalog?.models ?? []).filter((model) => model.available);
  return <DialogShell open={open} title={agent ? `Manage ${agent.agent_id}` : "Launch managed agent"} titleId="managed-agent-dialog-title" description="Router-managed agents run locally through the router." onClose={onClose} closeLabel="Close managed agent dialog" className="message-dialog managed-agent-dialog">
    {agent ? <section className="managed-lifecycle-actions" aria-label={`Actions for ${agent.agent_id}`}>
      <p>State: {agent.lifecycle_state} · {agent.activity_state}</p>
      <div className="dialog-actions"><button type="button" disabled={pending} onClick={() => { void onAction(agent.agent_id, "restart"); }}>Restart</button><button type="button" className="secondary" disabled={pending} onClick={() => { void onAction(agent.agent_id, "reset"); }}>Reset</button><button type="button" className="danger-button" disabled={pending} onClick={() => setConfirmTermination(true)}>Terminate</button></div>
      {confirmTermination ? <div className="managed-confirmation" role="alert"><p>Terminate {agent.agent_id}?</p><button type="button" className="danger-button" disabled={pending} onClick={() => { void onAction(agent.agent_id, "terminate"); }}>Terminate agent</button><button type="button" className="secondary" onClick={() => setConfirmTermination(false)}>Cancel</button></div> : null}
    </section> : <form className="managed-agent-form" onSubmit={submit}>
      <label>Name<input required value={name} onChange={(event) => setName(event.currentTarget.value)} autoComplete="off" /></label>
      <ManagedDirectoryPicker value={cwd} {...(recentFolders === undefined ? {} : { recentFolders })} onChange={setCwd} list={onListDirectories} />
      <fieldset><legend>Model (optional)</legend><ModelPicker models={availableModels} {...(selectedModel ? { currentModel: selectedModel } : {})} loading={catalogLoading} {...(catalog && availableModels.length === 0 ? { emptyMessage: "No connected models. Connect a provider in Provider settings." } : {})} pending={pending} disabled={pending} emptyOption={{ label: "Use router default", description: "Uses the router default when you launch.", onSelect: () => setSelectedModel(undefined) }} onChange={setSelectedModel} /></fieldset>
      {catalogError ? <p className="error" role="alert">{catalogError}</p> : null}
      <label>Initial instruction (optional)<textarea value={instruction} onChange={(event) => setInstruction(event.currentTarget.value)} /></label>
      <div className="dialog-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="submit" disabled={pending}>Launch</button></div>
    </form>}
    {error ? <p className="error" role="alert">{error}</p> : null}
  </DialogShell>;
}
