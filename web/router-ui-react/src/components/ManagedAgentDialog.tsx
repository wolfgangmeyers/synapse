import { useEffect, useState, type FormEvent, type ReactElement } from "react";
import type { ManagedAgent } from "../protocol/types";
import { DialogShell } from "./DialogShell";
import { ManagedDirectoryPicker } from "./ManagedDirectoryPicker";

export interface ManagedAgentDialogProps {
  open: boolean;
  agent?: ManagedAgent;
  pending?: boolean;
  error?: string;
  onClose(): void;
  onLaunch(input: { name: string; cwd: string; model?: { provider: string; id: string }; initial_instruction?: string }): Promise<void>;
  onAction(agentId: string, action: "restart" | "reset" | "terminate"): Promise<void>;
  onListDirectories(path: string): Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
}

export function ManagedAgentDialog({ open, agent, pending = false, error, onClose, onLaunch, onAction, onListDirectories }: ManagedAgentDialogProps): ReactElement {
  const [name, setName] = useState("");
  const [cwd, setCwd] = useState("");
  const [provider, setProvider] = useState("");
  const [modelID, setModelID] = useState("");
  const [instruction, setInstruction] = useState("");
  const [confirmTermination, setConfirmTermination] = useState(false);
  useEffect(() => { if (!open) return; setName(""); setCwd(agent?.cwd ?? ""); setProvider(agent?.model.provider ?? ""); setModelID(agent?.model.id ?? ""); setInstruction(""); setConfirmTermination(false); }, [agent, open]);
  const submit = (event: FormEvent<HTMLFormElement>): void => {
    event.preventDefault();
    void onLaunch({ name: name.trim(), cwd: cwd.trim(), ...(provider.trim() && modelID.trim() ? { model: { provider: provider.trim(), id: modelID.trim() } } : {}), ...(instruction.trim() ? { initial_instruction: instruction } : {}) });
  };
  return <DialogShell open={open} title={agent ? `Manage ${agent.agent_id}` : "Launch managed agent"} titleId="managed-agent-dialog-title" description="Router-managed agents run locally through the router." onClose={onClose} closeLabel="Close managed agent dialog" className="message-dialog managed-agent-dialog">
    {agent ? <section className="managed-lifecycle-actions" aria-label={`Actions for ${agent.agent_id}`}>
      <p>State: {agent.lifecycle_state} · {agent.activity_state}</p>
      <div className="dialog-actions"><button type="button" disabled={pending} onClick={() => { void onAction(agent.agent_id, "restart"); }}>Restart</button><button type="button" className="secondary" disabled={pending} onClick={() => { void onAction(agent.agent_id, "reset"); }}>Reset</button><button type="button" className="danger-button" disabled={pending} onClick={() => setConfirmTermination(true)}>Terminate</button></div>
      {confirmTermination ? <div className="managed-confirmation" role="alert"><p>Terminate {agent.agent_id}?</p><button type="button" className="danger-button" disabled={pending} onClick={() => { void onAction(agent.agent_id, "terminate"); }}>Terminate agent</button><button type="button" className="secondary" onClick={() => setConfirmTermination(false)}>Cancel</button></div> : null}
    </section> : <form className="managed-agent-form" onSubmit={submit}>
      <label>Name<input required value={name} onChange={(event) => setName(event.currentTarget.value)} autoComplete="off" /></label>
      <ManagedDirectoryPicker value={cwd} onChange={setCwd} list={onListDirectories} />
      <fieldset><legend>Model (optional)</legend><label>Provider<input value={provider} onChange={(event) => setProvider(event.currentTarget.value)} autoComplete="off" /></label><label>Model ID<input value={modelID} onChange={(event) => setModelID(event.currentTarget.value)} autoComplete="off" /></label></fieldset>
      <label>Initial instruction (optional)<textarea value={instruction} onChange={(event) => setInstruction(event.currentTarget.value)} /></label>
      <div className="dialog-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button type="submit" disabled={pending}>Launch</button></div>
    </form>}
    {error ? <p className="error" role="alert">{error}</p> : null}
  </DialogShell>;
}
