import { useEffect, useRef, useState, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import { ToolCategoryList } from "./ToolCategoryList";
import { awaitPermissionWrites, acknowledgePermissionError, enqueuePermissionWrite } from "./tool-permission-queue";
import type { RouterHttpClient, ToolDefinitionItem } from "../transport/http-client";

type Props = { open: boolean; client: RouterHttpClient; onClose: () => void } &
  ({ mode: "global" } | { mode: "agent"; agentId: string; agentName?: string | undefined; nativeToolsUnrestricted?: boolean | undefined });
type Tool = ToolDefinitionItem & { effective_enabled?: boolean };
type Data = { tools: Tool[]; workflowOverride: boolean; workflowState: string };
const nativeTools = new Set(["bash", "read", "edit", "write", "mecha_request_inline_tool_allowance"]);

export function ToolPermissionsDialog(props: Props): ReactElement {
  const { open, client, onClose, mode } = props;
  const agentId = mode === "agent" ? props.agentId : "";
  const unrestricted = mode === "agent" && Boolean(props.nativeToolsUnrestricted);
  const key = mode === "agent" ? `agent:${agentId}` : "global";
  const generation = useRef(0);
  const [data, setData] = useState<Data | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [writeError, setWriteError] = useState<string | null>(null);
  const [updatingCategory, setUpdatingCategory] = useState<string | null>(null);
  const [updatingTool, setUpdatingTool] = useState<string | null>(null);

  const load = async (token: number): Promise<void> => {
    if (generation.current !== token) return;
    setLoading(true); setError(null);
    try {
      if (mode === "agent") await awaitPermissionWrites(client, "global");
      const pendingError = await awaitPermissionWrites(client, key);
      if (generation.current !== token) return;
      setWriteError(pendingError ?? null);
      const result = mode === "agent" ? await client.getAgentTools(agentId) : await client.getToolDefaults();
      if (generation.current !== token) return;
      setData(Array.isArray(result)
        ? { tools: result, workflowOverride: false, workflowState: "" }
        : { tools: result.tools, workflowOverride: result.workflow_override, workflowState: result.active_workflow_state });
    } catch (err) {
      if (generation.current === token) { setData(null); setError(err instanceof Error ? err.message : "Failed to load tool permissions"); }
    } finally {
      if (generation.current === token) setLoading(false);
    }
  };

  useEffect(() => {
    const token = ++generation.current;
    setData(null); setError(null); setWriteError(null); setUpdatingCategory(null); setUpdatingTool(null);
    if (open && (mode === "global" || agentId)) void load(token);
    return () => { generation.current++; };
  }, [open, client, key]);

  const tools = (data?.tools ?? []).filter(t => !unrestricted || !nativeTools.has(t.tool_name));
  const change = async (names: string[], enabled: boolean, category?: string): Promise<void> => {
    const token = generation.current;
    if (category === undefined) setUpdatingTool(names[0] ?? null); else setUpdatingCategory(category);
    setError(null);
    setData(prev => prev && { ...prev, tools: prev.tools.map(t => names.includes(t.tool_name)
      ? { ...t, enabled, ...(mode === "agent" && !prev.workflowOverride ? { effective_enabled: enabled } : {}) } : t) });
    try {
      await enqueuePermissionWrite(client, key, async () => {
        for (const name of names) {
          if (mode === "agent") await client.updateAgentTools(agentId, { tool_name: name, enabled });
          else await client.updateToolDefaults({ tool_name: name, enabled });
        }
      });
    } catch {
      if (generation.current === token) await load(token);
    } finally {
      if (generation.current === token) { setUpdatingCategory(null); setUpdatingTool(null); }
    }
  };
  const title = mode === "global" ? "Global Tool Defaults" : `Manage Tools: ${props.agentName || agentId}`;
  return <DialogShell open={open} title={title} titleId={mode === "global" ? "tool-defaults-title" : "worker-tools-title"}
    description={mode === "global" ? "Configure defaults for tools without an agent-specific override, including existing agents." : "Configure tool access permissions for this worker."}
    onClose={onClose} closeLabel={mode === "global" ? "Close tool defaults" : "Close worker tools"}
    className={`message-dialog ${mode === "global" ? "tool-defaults-dialog" : "worker-tools-dialog"}`}>
    <div className={mode === "global" ? "tool-defaults-container" : "worker-tools-container"} style={{ display: "flex", flexDirection: "column", gap: "16px", maxHeight: "70vh", overflowY: "auto" }}>
      {unrestricted ? <p>These settings apply to Mecha tools only. Claude native tools are not restricted by worker or workflow settings.</p> : null}
      {error ? <div className="dialog-error" role="alert">{error} <button type="button" onClick={() => { void load(generation.current); }}>Retry</button></div> : null}
      {writeError ? <div className="dialog-error" role="alert">{writeError}{data ? <> Current saved settings are shown. <button type="button" onClick={() => { acknowledgePermissionError(client, key); setWriteError(null); }}>Dismiss</button></> : " Reload to see the saved settings."}</div> : null}
      {data?.workflowOverride ? <div className="workflow-override-banner" role="alert" style={{ display: "flex", alignItems: "center", gap: "10px", padding: "10px 14px", background: "rgba(255, 170, 0, 0.12)", border: "1px solid rgba(255, 170, 0, 0.3)", borderRadius: "6px", color: "var(--color-warning, #ffb86c)", fontSize: "0.85rem" }}>
        <i className="fa-solid fa-triangle-exclamation" aria-hidden="true" style={{ fontSize: "1.1rem" }} />
        <div>Workflow state &apos;<strong>{data.workflowState}</strong>&apos; is active and currently overrides tool permissions. Individual worker tool toggles will apply when the workflow completes or clears.</div>
      </div> : null}
      {loading && !data ? <div>Loading {mode === "agent" ? "worker tools" : "tool defaults"}...</div>
        : !data ? null : tools.length === 0 ? <div>No tools found{mode === "agent" ? " for this worker" : ""}.</div>
        : <ToolCategoryList tools={tools} workflowOverride={data.workflowOverride} updatingCategory={writeError ? "__error__" : updatingCategory} updatingTool={updatingTool} mode={mode}
          onCategoryToggle={(category, enabled) => { void change(tools.filter(t => (t.category || "General") === category).map(t => t.tool_name), enabled, category); }}
          onToolToggle={(name, enabled) => { void change([name], enabled); }} />}
      <div style={{ display: "flex", justifyContent: "flex-end", marginTop: "8px" }}><button type="button" className="secondary" onClick={onClose}>Close</button></div>
    </div>
  </DialogShell>;
}
