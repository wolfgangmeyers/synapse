import { useCallback, useEffect, useRef, useState } from "react";
import type { Agent } from "../state/agent-projection";
import type { RouterScope } from "../state/scopes";
import type { RouterTransport } from "../transport/router-transport";

export interface AgentReconciliation {
  agents: Agent[];
  ready: boolean;
  loading: boolean;
  error?: Error;
  refresh(): Promise<void>;
}
export function useAgentReconciliation(transport: RouterTransport, scope?: RouterScope, intervalMs = 3_000): AgentReconciliation {
  const scopeKey = scope === undefined ? transport.http.routerUrl : `${scope.routerUrl}\u001f${scope.agentId}`;
  const [agents, setAgents] = useState<Agent[]>([]); const [agentsScopeKey, setAgentsScopeKey] = useState(scopeKey); const [ready, setReady] = useState(false); const [loading, setLoading] = useState(false); const [error, setError] = useState<Error | undefined>(); const requestGeneration = useRef(0);
  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current; setLoading(true); try { await transport.refreshAgents(); if (generation !== requestGeneration.current) return; setError(undefined); } catch (reason) { if (generation !== requestGeneration.current) return; setError(reason instanceof Error ? reason : new Error("Agent refresh failed")); } finally { if (generation === requestGeneration.current) setLoading(false); }
  }, [transport]);
  useEffect(() => {
    setAgentsScopeKey(scopeKey); setAgents([]); setReady(false); setError(undefined);
    const unsubscribe = transport.subscribe((event) => {
      if (event.type !== "agents") return;
      setReady(true);
      if (scope !== undefined && (transport.http.routerUrl !== scope.routerUrl || event.agents.some((agent) => agent.agent_id === scope.agentId))) { setAgents(event.agents.filter((agent) => agent.agent_id !== scope.agentId)); return; }
      setAgents(event.agents);
    });
    void refresh(); const timer = setInterval(() => { void refresh(); }, intervalMs);
    return () => { unsubscribe(); clearInterval(timer); };
  }, [transport, scopeKey, scope?.routerUrl, scope?.agentId, intervalMs, refresh]);
  const scopedError = agentsScopeKey === scopeKey ? error : undefined;
  return { agents: agentsScopeKey === scopeKey ? agents : [], ready: agentsScopeKey === scopeKey && ready, loading, ...(scopedError === undefined ? {} : { error: scopedError }), refresh };
}
