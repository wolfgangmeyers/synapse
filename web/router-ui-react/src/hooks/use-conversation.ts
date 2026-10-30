import { useCallback, useEffect, useRef, useState } from "react";
import { appendConversation, reconcileConversation } from "../state/conversation-projection";
import { CONVERSATION_UNLOAD_THRESHOLD, DEFAULT_CONVERSATION_CHUNK_LIMIT, type HistoryMessage } from "../domain/history";
import type { RouterTransport } from "../transport/router-transport";
import type { ConversationTimelineEvent } from "../transport/http-client";
import type { RouterScope } from "../state/scopes";

export interface ConversationHook {
  messages: HistoryMessage[];
  timelineEvents: ConversationTimelineEvent[];
  events: ConversationTimelineEvent[];
  loading: boolean;
  loadingOlder: boolean;
  hasOlder: boolean;
  error?: Error;
  refresh(): Promise<void>;
  loadOlder(): Promise<void>;
  send(message: unknown): Promise<Record<string, unknown>>;
}
export function useConversation(transport: RouterTransport, scope: RouterScope, peer: string | undefined, onReconcile?: (peer: string, messages: HistoryMessage[], highWaterSequence: number) => void): ConversationHook {
  const scopeKey = `${scope.routerUrl}\u001f${scope.agentId}\u001f${peer ?? ""}`;
  const [messages, setMessages] = useState<HistoryMessage[]>([]);
  const [timelineEvents, setTimelineEvents] = useState<ConversationTimelineEvent[]>([]);
  const [messagesScopeKey, setMessagesScopeKey] = useState(scopeKey);
  const [loading, setLoading] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [nextBefore, setNextBefore] = useState<number | null | undefined>(undefined);
  const [nextBeforeSeq, setNextBeforeSeq] = useState<number | null | undefined>(undefined);
  const [error, setError] = useState<Error | undefined>();
  const requestGeneration = useRef(0);
  const previousScope = useRef({ routerUrl: scope.routerUrl, agentId: scope.agentId, peer });
  const freshHistoryScope = useRef<string | undefined>(undefined);
  // effectiveLoading: Computes loading state synchronously during peer/agent switches
  // to prevent UI flicker/stale conversation flashes before the async refresh effect kicks in.
  const lastRenderedPeer = useRef(peer);
  const effectiveLoading = (lastRenderedPeer.current !== peer && peer !== undefined && peer !== "") || loading;
  lastRenderedPeer.current = peer;

  const refresh = useCallback(async () => {
    const generation = ++requestGeneration.current;
    if (peer === undefined || peer === "") { setMessages([]); setTimelineEvents([]); setNextBefore(undefined); setNextBeforeSeq(undefined); return; }
    setLoading(true);
    const useFreshHistory = freshHistoryScope.current === scopeKey;

    // First try the unified timeline endpoint GET /v1/conversation?agent_id=...&limit=DEFAULT_CONVERSATION_CHUNK_LIMIT
    if (typeof transport.http?.fetchConversationTimeline === "function") {
      try {
        const result = await transport.http.fetchConversationTimeline(peer, DEFAULT_CONVERSATION_CHUNK_LIMIT);
        if (generation !== requestGeneration.current) return;
        setTimelineEvents(result.events);
        setNextBeforeSeq(result.next_before_seq);
        setNextBefore(result.next_before_seq);

        // Dual normalization: support from/sender and to/recipient in timeline event metadata
        // Role inversion: Map router timeline roles ('user'/'assistant') or sender identity to conversation direction
        // (outgoing vs incoming) relative to the local agent/user scope.
        const projectedMessages: HistoryMessage[] = result.events.filter((e) => e.kind === "message").map((e) => {
          const fromMeta = typeof e.metadata?.from === "string"
            ? e.metadata.from
            : (typeof e.metadata?.sender === "string" ? e.metadata.sender : undefined);
          const toMeta = typeof e.metadata?.to === "string"
            ? e.metadata.to
            : (typeof e.metadata?.recipient === "string" ? e.metadata.recipient : undefined);
          const isOutgoing = e.role === "user" || fromMeta === scope.agentId;
          return {
            id: e.id,
            body: e.content ?? "",
            direction: isOutgoing ? "outgoing" : "incoming",
            from: fromMeta ?? (isOutgoing ? scope.agentId : peer),
            to: toMeta ?? (isOutgoing ? peer : scope.agentId),
            sequence: e.seq,
            created_at: new Date(e.created_at).toISOString(),
            recorded_at: new Date(e.created_at).toISOString(),
            ...(e.metadata && typeof e.metadata === "object" ? e.metadata : {}),
          };
        });

        setMessages((current) => {
          const reconciled = reconcileConversation({ messages: useFreshHistory ? [] : current, highWaterSequence: 0, followMessages: true }, projectedMessages, 0).messages;
          if (useFreshHistory) freshHistoryScope.current = undefined;
          onReconcile?.(peer, reconciled, 0);
          return reconciled;
        });
        setError(undefined);
        return;
      } catch {
        // Fall back to transport.conversation for backwards compatibility
      } finally {
        if (generation === requestGeneration.current) setLoading(false);
      }
    }

    try {
      const result = await transport.conversation(scope.agentId, peer, { limit: DEFAULT_CONVERSATION_CHUNK_LIMIT });
      if (generation !== requestGeneration.current) return;
      setNextBefore(result.next_before ?? null);
      setMessages((current) => {
        const reconciled = reconcileConversation({ messages: useFreshHistory ? [] : current, highWaterSequence: 0, followMessages: true }, result.messages, result.high_water_sequence).messages;
        if (useFreshHistory) freshHistoryScope.current = undefined;
        onReconcile?.(peer, reconciled, result.high_water_sequence);
        return reconciled;
      });
      setError(undefined);
    } catch (reason) {
      if (generation !== requestGeneration.current) return;
      setError(reason instanceof Error ? reason : new Error("Conversation refresh failed"));
    } finally {
      if (generation === requestGeneration.current) setLoading(false);
    }
  }, [transport, peer, scope.routerUrl, scope.agentId, scopeKey, onReconcile]);

  const loadOlder = useCallback(async () => {
    if (peer === undefined || peer === "" || loadingOlder) return;

    if (timelineEvents.length > 0 && (typeof transport.http?.fetchConversationTimeline === "function" || typeof transport.fetchConversationTimeline === "function")) {
      const oldestSeq = timelineEvents[0]?.seq ?? nextBeforeSeq;
      if (oldestSeq !== null && oldestSeq !== undefined && oldestSeq > 0) {
        setLoadingOlder(true);
        try {
          const result = await transport.http.fetchConversationTimeline(peer, DEFAULT_CONVERSATION_CHUNK_LIMIT, oldestSeq);
          setTimelineEvents((prev) => [...result.events, ...prev]);
          setNextBeforeSeq(result.next_before_seq);
          setNextBefore(result.next_before_seq);

          // Dual normalization: support from/sender and to/recipient in timeline event metadata
          const olderMessages: HistoryMessage[] = result.events.filter((e) => e.kind === "message").map((e) => {
            const fromMeta = typeof e.metadata?.from === "string"
              ? e.metadata.from
              : (typeof e.metadata?.sender === "string" ? e.metadata.sender : undefined);
            const toMeta = typeof e.metadata?.to === "string"
              ? e.metadata.to
              : (typeof e.metadata?.recipient === "string" ? e.metadata.recipient : undefined);
            const isOutgoing = e.role === "user" || fromMeta === scope.agentId;
            return {
              id: e.id,
              body: e.content ?? "",
              direction: isOutgoing ? "outgoing" : "incoming",
              from: fromMeta ?? (isOutgoing ? scope.agentId : peer),
              to: toMeta ?? (isOutgoing ? peer : scope.agentId),
              sequence: e.seq,
              created_at: new Date(e.created_at).toISOString(),
              recorded_at: new Date(e.created_at).toISOString(),
              ...(e.metadata && typeof e.metadata === "object" ? e.metadata : {}),
            };
          });

          setMessages((current) => {
            const reconciled = reconcileConversation({ messages: current, highWaterSequence: 0, followMessages: true }, olderMessages, 0).messages;
            return reconciled;
          });
          return;
        } catch {
          // fall through
        } finally {
          setLoadingOlder(false);
        }
      }
    }

    if (nextBefore === null || nextBefore === undefined || nextBefore <= 0) return;
    setLoadingOlder(true);
    try {
      const result = await transport.conversation(scope.agentId, peer, { before: nextBefore, limit: DEFAULT_CONVERSATION_CHUNK_LIMIT });
      setNextBefore(result.next_before ?? null);
      setMessages((current) => {
        const reconciled = reconcileConversation({ messages: current, highWaterSequence: 0, followMessages: true }, result.messages, result.high_water_sequence).messages;
        return reconciled;
      });
    } catch {
      // ignore
    } finally {
      setLoadingOlder(false);
    }
  }, [peer, nextBefore, nextBeforeSeq, timelineEvents, loadingOlder, transport, scope.agentId]);

  useEffect(() => {
    const prior = previousScope.current;
    const sameConversation = prior.routerUrl === scope.routerUrl && prior.peer === peer;
    if (!sameConversation) {
      setMessages([]);
      setTimelineEvents([]);
      setNextBefore(undefined);
      setNextBeforeSeq(undefined);
      if (peer !== undefined && peer !== "") {
        setLoading(true);
      }
    }
    freshHistoryScope.current = sameConversation && prior.agentId !== scope.agentId ? scopeKey : undefined;
    previousScope.current = { routerUrl: scope.routerUrl, agentId: scope.agentId, peer };
    setMessagesScopeKey(scopeKey);
    setError(undefined);
    let active = true;
    const unsubscribe = transport.subscribe((event) => {
      if (!active) return;
      if (event.type === "status") { if (event.status === "connected" && event.identity.agentId === scope.agentId) void refresh(); return; }
      if (event.type !== "conversation" && event.type !== "delivery") return;
      const message = event.message;
      if (peer !== undefined && message.from !== peer && message.to !== peer) return;
      // Dual normalization: support sender/recipient across from/sender and to/recipient properties
      const msgFrom = message.from ?? (message as { sender?: string }).sender;
      const msgTo = message.to ?? (message as { recipient?: string }).recipient;
      const addressed = msgFrom === scope.agentId ? msgTo : msgTo === scope.agentId ? msgFrom : (msgFrom === peer ? msgTo : msgFrom);
      setTimelineEvents((current) => {
        const lastSeq = current.length > 0 ? (current[current.length - 1]?.seq ?? 0) : 0;
        const liveEvt: ConversationTimelineEvent = {
          id: `msg:${peer ?? ""}:${message.id}`,
          agent_id: peer ?? "",
          seq: message.sequence ?? (lastSeq + 1),
          kind: "message",
          role: msgFrom === scope.agentId ? "user" : "assistant",
          ...(message.body !== undefined ? { content: message.body } : {}),
          metadata: {
            ...(msgFrom !== undefined ? { from: msgFrom, sender: msgFrom } : {}),
            ...(msgTo !== undefined ? { to: msgTo, recipient: msgTo } : {}),
            ...(message.subject !== undefined ? { subject: message.subject } : {}),
            ...(Array.isArray(message.attachments) ? { attachments: message.attachments } : {}),
          },
          created_at: Date.now(),
        };
        const next = [...current, liveEvt];
        return next.length > CONVERSATION_UNLOAD_THRESHOLD ? next.slice(-CONVERSATION_UNLOAD_THRESHOLD) : next;
      });
      setMessages((current) => {
        let next: HistoryMessage[];
        if (current.some((m) => m.id === message.id)) {
          next = current.map((m) => m.id === message.id ? { ...m, ...message } : m);
        } else {
          const useFreshMessages = freshHistoryScope.current === scopeKey;
          const appended = appendConversation({ messages: useFreshMessages ? [] : current, highWaterSequence: 0, followMessages: true }, message).messages;
          next = useFreshMessages ? appendConversation({ messages: current, highWaterSequence: 0, followMessages: true }, message).messages : appended;
        }
        if (next.length > CONVERSATION_UNLOAD_THRESHOLD) {
          const trimmed = next.slice(-CONVERSATION_UNLOAD_THRESHOLD);
          onReconcile?.(peer ?? "", trimmed, 0);
          return trimmed;
        }
        return next;
      });
    });
    void refresh();
    return () => { active = false; requestGeneration.current += 1; unsubscribe(); };
  }, [transport, scopeKey, scope.routerUrl, scope.agentId, peer, refresh]);

  const send = useCallback(async (message: unknown): Promise<Record<string, unknown>> => transport.sendMessage(message), [transport]);
  const scopedError = messagesScopeKey === scopeKey ? error : undefined;
  const oldestEventSeq = timelineEvents.length > 0 && timelineEvents[0] ? timelineEvents[0].seq : undefined;
  const hasOlder = (oldestEventSeq !== undefined && oldestEventSeq !== null && oldestEventSeq > 1)
    || (nextBeforeSeq !== null && nextBeforeSeq !== undefined && nextBeforeSeq > 0)
    || (nextBefore !== null && nextBefore !== undefined && nextBefore > 0);
  return {
    messages: messagesScopeKey === scopeKey ? messages : [],
    timelineEvents: messagesScopeKey === scopeKey ? timelineEvents : [],
    events: messagesScopeKey === scopeKey ? timelineEvents : [],
    loading: effectiveLoading,
    loadingOlder,
    hasOlder,
    ...(scopedError === undefined ? {} : { error: scopedError }),
    refresh,
    loadOlder,
    send,
  };
}
