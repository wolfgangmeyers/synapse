import { useCallback, useEffect, useRef, useState } from "react";
import { appendConversation, reconcileConversation } from "../state/conversation-projection";
import type { HistoryMessage } from "../domain/history";
import type { RouterTransport } from "../transport/router-transport";
import type { RouterScope } from "../state/scopes";

export interface ConversationHook {
  messages: HistoryMessage[];
  loading: boolean;
  error?: Error;
  refresh(): Promise<void>;
  send(message: unknown): Promise<Record<string, unknown>>;
}
export function useConversation(transport: RouterTransport, scope: RouterScope, peer: string | undefined, onPersist?: (peer: string, messages: HistoryMessage[]) => void, onReconcile?: (peer: string, messages: HistoryMessage[], highWaterSequence: number) => void): ConversationHook {
  const scopeKey = `${scope.routerUrl}\u001f${scope.agentId}\u001f${peer ?? ""}`;
  const [messages, setMessages] = useState<HistoryMessage[]>([]); const [messagesScopeKey, setMessagesScopeKey] = useState(scopeKey); const [loading, setLoading] = useState(false); const [error, setError] = useState<Error | undefined>(); const requestGeneration = useRef(0); const previousScope = useRef({ routerUrl: scope.routerUrl, agentId: scope.agentId, peer }); const freshHistoryScope = useRef<string | undefined>(undefined);
  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current;
    if (peer === undefined || peer === "") { setMessages([]); return; }
    setLoading(true); const useFreshHistory = freshHistoryScope.current === scopeKey; try { const result = await transport.conversation(scope.agentId, peer); if (generation !== requestGeneration.current) return; setMessages((current) => { const reconciled = reconcileConversation({ messages: useFreshHistory ? [] : current, highWaterSequence: 0, followMessages: true }, result.messages, result.high_water_sequence).messages; if (useFreshHistory) freshHistoryScope.current = undefined; onReconcile?.(peer, reconciled, result.high_water_sequence); onPersist?.(peer, reconciled); return reconciled; }); setError(undefined); } catch (reason) { if (generation !== requestGeneration.current) return; setError(reason instanceof Error ? reason : new Error("Conversation refresh failed")); } finally { if (generation === requestGeneration.current) setLoading(false); }
  }, [transport, peer, scope.routerUrl, scope.agentId, scopeKey, onPersist, onReconcile]);
  useEffect(() => {
    const prior = previousScope.current; const sameConversation = prior.routerUrl === scope.routerUrl && prior.peer === peer;
    if (!sameConversation) setMessages([]);
    freshHistoryScope.current = sameConversation && prior.agentId !== scope.agentId ? scopeKey : undefined;
    previousScope.current = { routerUrl: scope.routerUrl, agentId: scope.agentId, peer };
    setMessagesScopeKey(scopeKey); setError(undefined);
    let active = true; const unsubscribe = transport.subscribe((event) => {
      if (!active) return;
      if (event.type === "status") { if (event.status === "connected" && event.identity.agentId === scope.agentId) void refresh(); return; }
      if (event.type !== "conversation" && event.type !== "delivery") return;
      const message = event.message; const addressed = message.from === scope.agentId ? message.to : message.to === scope.agentId ? message.from : undefined;
      if (addressed === undefined) return;
      setMessages((current) => {
        if (peer !== undefined && addressed !== peer) return current;
        const useFreshMessages = freshHistoryScope.current === scopeKey;
        const next = appendConversation({ messages: useFreshMessages ? [] : current, highWaterSequence: 0, followMessages: true }, message).messages;
        if (addressed === peer) onPersist?.(addressed, useFreshMessages ? [message] : next);
        return useFreshMessages ? appendConversation({ messages: current, highWaterSequence: 0, followMessages: true }, message).messages : next;
      });
      if (addressed !== peer) onPersist?.(addressed, [message]);
    });
    void refresh(); const timer = peer === undefined ? undefined : setTimeout(() => { void refresh(); }, 500);
    return () => { active = false; requestGeneration.current += 1; unsubscribe(); if (timer !== undefined) clearTimeout(timer); };
  }, [transport, scopeKey, scope.routerUrl, scope.agentId, peer, refresh, onPersist]);
  const send = useCallback(async (message: unknown): Promise<Record<string, unknown>> => transport.sendMessage(message), [transport]);
  const scopedError = messagesScopeKey === scopeKey ? error : undefined;
  return { messages: messagesScopeKey === scopeKey ? messages : [], loading, ...(scopedError === undefined ? {} : { error: scopedError }), refresh, send };
}
