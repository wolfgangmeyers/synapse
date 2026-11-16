import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { appendConversation, reconcileConversation } from "../state/conversation-projection";
import { CONVERSATION_UNLOAD_THRESHOLD, DEFAULT_CONVERSATION_CHUNK_LIMIT, mergeMessages, type HistoryMessage } from "../domain/history";
import type { RouterTransport } from "../transport/router-transport";
import type { ConversationTimelineEvent } from "../transport/http-client";
import type { UILiveClient } from "../transport/ui-live-client";
import type { ManagedHistoryEntry } from "../protocol/types";
import { compareManagedHistoryEntries } from "../state/reducer";
import type { RouterScope } from "../state/scopes";

function projectTimelineMessages(events: ConversationTimelineEvent[], owner: string, peer: string): HistoryMessage[] {
  return events.filter((event) => event.kind === "message").map((event) => {
    const from = typeof event.metadata?.from === "string" ? event.metadata.from : typeof event.metadata?.sender === "string" ? event.metadata.sender : undefined;
    const to = typeof event.metadata?.to === "string" ? event.metadata.to : typeof event.metadata?.recipient === "string" ? event.metadata.recipient : undefined;
    const outgoing = event.role === "user" || from === owner;
    return { ...event.metadata, id: event.id.startsWith(`msg:${peer}:`) ? event.id.slice(`msg:${peer}:`.length) : event.id, body: event.content ?? "", direction: outgoing ? "outgoing" : "incoming", from: from ?? (outgoing ? owner : peer), to: to ?? (outgoing ? peer : owner), sequence: event.seq, created_at: new Date(event.created_at).toISOString(), recorded_at: new Date(event.created_at).toISOString() };
  });
}

export interface ConversationHook {
  messages: HistoryMessage[];
  timelineEvents: ConversationTimelineEvent[];
  liveTimelineIds: ReadonlySet<string>;
  events: ConversationTimelineEvent[];
  activityEntries: ManagedHistoryEntry[];
  loading: boolean;
  loadingOlder: boolean;
  hasOlder: boolean;
  error?: Error;
  discardBefore(ids: ReadonlySet<string>): void;
  refresh(): Promise<void>;
  loadOlder(beforeCommit?: () => void): Promise<void>;
  send(message: unknown, signal?: AbortSignal): Promise<Record<string, unknown>>;
}
export function useConversation(transport: RouterTransport, scope: RouterScope, peer: string | undefined, onReconcile?: (peer: string, messages: HistoryMessage[], highWaterSequence: number) => void, uiLive?: UILiveClient, selectionIdentity = peer): ConversationHook {
  const scopeKey = `${scope.routerUrl}\u001f${scope.agentId}\u001f${selectionIdentity ?? ""}\u001f${peer ?? ""}`;
  const [liveTimeline, setLiveTimeline] = useState<{ key: string; ids: ReadonlySet<string> }>({ key: scopeKey, ids: new Set() });
  const [messages, setMessages] = useState<HistoryMessage[]>([]);
  const [activityEntries, setActivityEntries] = useState<ManagedHistoryEntry[]>([]);
  const messagesRef = useRef(messages);
  messagesRef.current = messages;
  const reconnectBoundary = useRef<{ events: Set<string>; messages: Set<string> } | null>(null);
  const liveEvents = useRef(new Map<string, ConversationTimelineEvent>());
  const refreshing = useRef(false);
  const repairQueued = useRef(false);
  const carryLive = useRef(false);
  const liveMessages = useRef(new Map<string, HistoryMessage>());
  const [timelineWindow, setTimelineWindow] = useState<{ events: ConversationTimelineEvent[]; trimmed: boolean }>({ events: [], trimmed: false });
  const timelineEvents = timelineWindow.events;
  const timelineWindowRef = useRef(timelineWindow);
  timelineWindowRef.current = timelineWindow;
  const [messagesScopeKey, setMessagesScopeKey] = useState(scopeKey);
  const [loading, setLoading] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [nextBefore, setNextBefore] = useState<number | null | undefined>(undefined);
  const [nextBeforeSeq, setNextBeforeSeq] = useState<number | null | undefined>(undefined);
  const [error, setError] = useState<Error | undefined>();
  const requestGeneration = useRef(0);
  // Pagination belongs to a conversation lifetime, not an individual refresh.
  const conversationEpoch = useRef(0);
  const olderRequest = useRef<object | null>(null);
  const windowGeneration = useRef(0);
  const discardedCursor = useRef(false);
  const expanded = useRef(false);
  const initialized = useRef(false);
  useLayoutEffect(() => {
    conversationEpoch.current += 1;
    setLiveTimeline({ key: scopeKey, ids: new Set() });
    requestGeneration.current += 1;
    refreshing.current = false;
    repairQueued.current = false;
    carryLive.current = false;
    reconnectBoundary.current = null;
    liveEvents.current.clear();
    liveMessages.current.clear();
    olderRequest.current = null;
    windowGeneration.current += 1;
    discardedCursor.current = false;
    expanded.current = false;
    initialized.current = false;
    setLoadingOlder(false);
    return () => {
      conversationEpoch.current += 1;
      olderRequest.current = null;
    };
  }, [scopeKey, transport]);
  const previousScope = useRef(scopeKey);
  // effectiveLoading: Computes loading state synchronously during peer/agent switches
  // to prevent UI flicker/stale conversation flashes before the async refresh effect kicks in.
  const lastRenderedPeer = useRef(peer);
  const effectiveLoading = (lastRenderedPeer.current !== peer && peer !== undefined && peer !== "") || loading;
  lastRenderedPeer.current = peer;

  const refresh = useCallback(async () => {
    if (refreshing.current) { carryLive.current = true; }
    refreshing.current = true;
    const generation = ++requestGeneration.current;
    const boundary = reconnectBoundary.current ?? { events: new Set(timelineWindowRef.current.events.map((event) => event.id)), messages: new Set(messagesRef.current.map((message) => message.id)) };
    if (peer === undefined || peer === "") { setMessages([]); setTimelineWindow({ events: [], trimmed: false }); setNextBefore(undefined); setNextBeforeSeq(undefined); setLoading(false); refreshing.current = false; return; }
    setLoading(true);
    if (!carryLive.current) { liveEvents.current.clear(); liveMessages.current.clear(); }
    carryLive.current = false;
    olderRequest.current = null;
    setLoadingOlder(false);


    // First try the unified timeline endpoint GET /v1/conversation?agent_id=...&limit=DEFAULT_CONVERSATION_CHUNK_LIMIT
    if (typeof transport.http?.fetchConversationTimeline === "function") {
      try {
        const result = await transport.http.fetchConversationTimeline(peer, DEFAULT_CONVERSATION_CHUNK_LIMIT);
        if (generation !== requestGeneration.current) return;
        const same = initialized.current;
        const previous = same ? timelineWindowRef.current : { events: [], trimmed: false };
        const disconnected = previous.events.length > 0 && result.events.length > 0 && result.next_before_seq != null && !result.events.some((item) => boundary.events.has(item.id));
        const retained = disconnected ? { events: [], trimmed: false } : previous;
        const events = new Map(retained.events.map((event) => [event.id, event]));
        for (const event of result.events) events.set(event.id, event);
        const live = [...(disconnected ? previous.events.filter((event) => event.seq === 0 || !boundary.events.has(event.id)) : []), ...liveEvents.current.values()];
        for (const event of live) {
          const persisted = events.get(event.id);
          events.delete(event.id);
          events.set(event.id, persisted ? { ...persisted, ...event, seq: persisted.seq, created_at: persisted.created_at, metadata: { ...persisted.metadata, ...event.metadata } } : event);
        }
        const mergedEvents = [...events.values()].sort((a, b) => a.seq > 0 && b.seq > 0 ? a.seq - b.seq : a.seq > 0 ? -1 : b.seq > 0 ? 1 : a.created_at - b.created_at);
        const shouldTrim = !expanded.current && mergedEvents.length > CONVERSATION_UNLOAD_THRESHOLD;
        setTimelineWindow({ events: shouldTrim ? mergedEvents.slice(-CONVERSATION_UNLOAD_THRESHOLD) : mergedEvents, trimmed: retained.trimmed || shouldTrim });
        if (disconnected || (retained.events.length === 0 && !discardedCursor.current)) setNextBeforeSeq(result.next_before_seq);
        if (disconnected) discardedCursor.current = false;
        setNextBefore(undefined);

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

        onReconcile?.(peer, projectedMessages, 0);
        const liveMessageSnapshot = [...liveMessages.current.values()];
        setMessages((current) => {
          const merged = mergeMessages(mergeMessages(same ? (disconnected ? current.filter((message) => previous.events.some((event) => event.seq === 0 && (event.id === message.id || event.id === `msg:${peer}:${message.id}`))) : current) : [], projectedMessages.map((message) => ({ ...message, id: message.id.startsWith(`msg:${peer}:`) ? message.id.slice(`msg:${peer}:`.length) : message.id }))), liveMessageSnapshot);
          return expanded.current ? merged : merged.slice(-CONVERSATION_UNLOAD_THRESHOLD);
        });
        initialized.current = true;
        reconnectBoundary.current = null;
        setError(undefined);
        if (generation === requestGeneration.current) { setLoading(false); refreshing.current = false; if (repairQueued.current) { repairQueued.current = false; carryLive.current = true; void refresh(); } else { liveEvents.current.clear(); liveMessages.current.clear(); } }
        return;
      } catch (reason) {
        if (generation !== requestGeneration.current) return;
        if (reason instanceof Error && reason.message.includes("History request timed out")) { setError(reason); setLoading(false); refreshing.current = false; carryLive.current = true; return; }
        // Fall back to transport.conversation for backwards compatibility
      }
    }

    try {
      const result = await transport.conversation(scope.agentId, peer, { limit: DEFAULT_CONVERSATION_CHUNK_LIMIT });
      if (generation !== requestGeneration.current) return;
      setNextBeforeSeq(undefined);
      setTimelineWindow({ events: [], trimmed: false });
      const disconnected = initialized.current && boundary.messages.size > 0 && result.messages.length > 0 && result.next_before != null && !result.messages.some((message) => boundary.messages.has(message.id));
      setNextBefore((current) => disconnected || current === undefined ? result.next_before ?? null : current);
      onReconcile?.(peer, result.messages, result.high_water_sequence);
      const same = initialized.current;
      setMessages((current) => { const merged = mergeMessages(mergeMessages(same ? (disconnected ? current.filter((message) => !boundary.messages.has(message.id)) : current) : [], result.messages), [...liveMessages.current.values()]); return expanded.current ? merged : merged.slice(-CONVERSATION_UNLOAD_THRESHOLD); });
      initialized.current = true;
      reconnectBoundary.current = null;
      setError(undefined);
    } catch (reason) {
      if (generation !== requestGeneration.current) return;
      carryLive.current = true;
      setError(reason instanceof Error ? reason : new Error("Conversation refresh failed"));
    } finally {
      if (generation === requestGeneration.current) { setLoading(false); refreshing.current = false; if (repairQueued.current) { repairQueued.current = false; carryLive.current = true; void refresh(); } else if (!carryLive.current) { liveEvents.current.clear(); liveMessages.current.clear(); } }
    }
  }, [transport, peer, scope.routerUrl, scope.agentId, scopeKey, onReconcile]);

  const loadOlder = useCallback(async (beforeCommit?: () => void) => {
    if (peer === undefined || peer === "" || olderRequest.current !== null) return;
    const request = {};
    const epoch = conversationEpoch.current;
    const generation = requestGeneration.current;
    const window = windowGeneration.current;
    olderRequest.current = request;
    const isCurrent = () => conversationEpoch.current === epoch && requestGeneration.current === generation && windowGeneration.current === window && olderRequest.current === request;
    setLoadingOlder(true);
    try {
    if ((timelineEvents.length > 0 || (nextBeforeSeq != null && nextBeforeSeq > 0)) && (typeof transport.http?.fetchConversationTimeline === "function" || typeof transport.fetchConversationTimeline === "function")) {
      if (timelineWindow.trimmed) {
        // The live sequence space is not a durable cursor; resolve the retained ID on the router.
        try {
          const boundary = timelineEvents[0]?.id;
          if (!boundary) return;
          const page = await transport.http.fetchConversationTimeline(peer, DEFAULT_CONVERSATION_CHUNK_LIMIT, undefined, boundary);
          if (!isCurrent()) return;
          // A live trim may have removed the requested boundary while the page was pending.
          // Do not prepend across the resulting gap; retry from the new boundary.
          if (timelineWindowRef.current.events[0]?.id !== boundary) return;
          beforeCommit?.();
          expanded.current = true;
          setTimelineWindow((prev) => {
            const ids = new Set(prev.events.map((event) => event.id));
            return { events: [...page.events.filter((event) => !ids.has(event.id)), ...prev.events], trimmed: false };
          });
          setNextBeforeSeq(page.next_before_seq);
          setMessages((current) => mergeMessages(projectTimelineMessages(page.events, scope.agentId, peer), current));
          setError(undefined);
        } catch {
          if (isCurrent()) setError(new Error("Unable to recover older history. Please retry after the boundary is persisted."));
        }
        return;
      }
      const oldestSeq = nextBeforeSeq;
      if (oldestSeq !== null && oldestSeq !== undefined && oldestSeq > 0) {
        try {
          const boundary = timelineEvents[0]?.id;
          const result = await transport.http.fetchConversationTimeline(peer, DEFAULT_CONVERSATION_CHUNK_LIMIT, oldestSeq);
          if (!isCurrent() || timelineWindowRef.current.events[0]?.id !== boundary) return;
          beforeCommit?.();
          expanded.current = true;
          setTimelineWindow((prev) => { const ids = new Set(prev.events.map((event) => event.id)); return { events: [...result.events.filter((event) => !ids.has(event.id)), ...prev.events].sort((a, b) => a.seq > 0 && b.seq > 0 ? a.seq - b.seq : a.seq > 0 ? -1 : b.seq > 0 ? 1 : a.created_at - b.created_at), trimmed: false }; });
          setNextBeforeSeq(result.next_before_seq);
          setError(undefined);


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
          if (!isCurrent()) return;
          setError(new Error("Unable to load older timeline history. Please retry."));
          return;
        }
      }
    }

    if (nextBeforeSeq !== undefined) return;
    if (nextBefore === null || nextBefore === undefined || nextBefore <= 0) return;
    try {
      const result = await transport.conversation(scope.agentId, peer, { before: nextBefore, limit: DEFAULT_CONVERSATION_CHUNK_LIMIT });
      if (!isCurrent()) return;
      beforeCommit?.();
      expanded.current = true;
      setNextBefore(result.next_before ?? null);
      setError(undefined);
      setMessages((current) => {
        const reconciled = reconcileConversation({ messages: current, highWaterSequence: 0, followMessages: true }, result.messages, result.high_water_sequence).messages;
        return reconciled;
      });
    } catch {
      if (isCurrent()) setError(new Error("Unable to load older history. Please retry."));
    }
    } finally {
      if (isCurrent()) {
        olderRequest.current = null;
        setLoadingOlder(false);
      }
    }
  }, [peer, nextBefore, nextBeforeSeq, timelineEvents, timelineWindow.trimmed, transport, scope.agentId]);

  useEffect(() => {
    liveEvents.current.clear();
    setActivityEntries([]);
    const prior = previousScope.current;
    const sameConversation = prior === scopeKey;
    if (!sameConversation) {
      setMessages([]);
      setTimelineWindow({ events: [], trimmed: false });
      setNextBefore(undefined);
      setNextBeforeSeq(undefined);
      if (peer !== undefined && peer !== "") {
        setLoading(true);
      }
    }
    previousScope.current = scopeKey;
    setMessagesScopeKey(scopeKey);
    setError(undefined);
    let active = true;
    const unsubscribe = transport.subscribe((event) => {
      if (!active) return;
      if (event.type === "status") {
        if (event.status !== "connected" && reconnectBoundary.current === null) reconnectBoundary.current = { events: new Set(timelineWindowRef.current.events.map((item) => item.id)), messages: new Set(messagesRef.current.map((item) => item.id)) };
        if (event.status === "connected" && event.identity.agentId === scope.agentId) void refresh();
        return;
      }
      if (event.type !== "conversation" && event.type !== "delivery") return;
      const message = event.message;
      const msgFrom = message.from ?? (typeof message.sender === "string" ? message.sender : undefined);
      const msgTo = message.to ?? (typeof message.recipient === "string" ? message.recipient : undefined);
      const isUserAlias = (id?: string) => !id || id === scope.agentId || id === "user" || id === "wolfgang";
      const isIncoming = msgFrom === peer && isUserAlias(msgTo);
      const isOutgoing = msgTo === peer && isUserAlias(msgFrom);
      if (!peer || (!isIncoming && !isOutgoing)) return;
      if (refreshing.current) {
        if (!liveMessages.current.has(message.id) && liveMessages.current.size >= CONVERSATION_UNLOAD_THRESHOLD) liveMessages.current.delete(liveMessages.current.keys().next().value ?? "");
        liveMessages.current.set(message.id, message);
      }
      const liveId = `msg:${peer}:${message.id}`;
      setLiveTimeline((current) => {
        const ids = new Set(current.key === scopeKey ? current.ids : []);
        ids.delete(liveId);
        ids.add(liveId);
        while (ids.size > CONVERSATION_UNLOAD_THRESHOLD) ids.delete(ids.values().next().value ?? "");
        return { key: scopeKey, ids };
      });
      const liveEvent: ConversationTimelineEvent = { id: liveId, agent_id: peer, seq: 0, kind: "message", role: isOutgoing ? "user" : "assistant", content: message.body ?? "", metadata: { from: msgFrom, to: msgTo, ...(message.subject !== undefined ? { subject: message.subject } : {}), ...(Array.isArray(message.attachments) ? { attachments: message.attachments } : {}) }, created_at: Date.now() };
      if (refreshing.current && !liveEvents.current.has(liveId) && liveEvents.current.size >= CONVERSATION_UNLOAD_THRESHOLD) liveEvents.current.delete(liveEvents.current.keys().next().value ?? "");
      if (refreshing.current) liveEvents.current.set(liveId, liveEvent);
      setTimelineWindow((window) => {
        const current = window.events;
        const liveEvt: ConversationTimelineEvent = {
          id: `msg:${peer ?? ""}:${message.id}`,
          agent_id: peer ?? "",
          seq: 0,
          kind: "message",
          role: isOutgoing ? "user" : "assistant",
          ...(message.body !== undefined ? { content: message.body } : {}),
          metadata: {
            ...(msgFrom !== undefined ? { from: msgFrom, sender: msgFrom } : {}),
            ...(msgTo !== undefined ? { to: msgTo, recipient: msgTo } : {}),
            ...(message.subject !== undefined ? { subject: message.subject } : {}),
            ...(Array.isArray(message.attachments) ? { attachments: message.attachments } : {}),
          },
          created_at: Date.now(),
        };
        const existing = current.findIndex((event) => event.id === liveEvt.id);
        const next = existing < 0 ? [...current, liveEvt] : current.map((event, index) => index === existing ? { ...liveEvt, ...event, ...(liveEvt.content !== undefined ? { content: liveEvt.content } : {}), metadata: { ...event.metadata, ...liveEvt.metadata } } : event);
        return { events: next.length > CONVERSATION_UNLOAD_THRESHOLD ? next.slice(-CONVERSATION_UNLOAD_THRESHOLD) : next, trimmed: window.trimmed || next.length > CONVERSATION_UNLOAD_THRESHOLD };
      });
      setMessages((current) => {
        let next: HistoryMessage[];
        if (current.some((m) => m.id === message.id)) {
          next = current.map((m) => m.id === message.id ? { ...m, ...message } : m);
        } else {
          next = appendConversation({ messages: current, highWaterSequence: 0, followMessages: true }, message).messages;
        }
        return next.length > CONVERSATION_UNLOAD_THRESHOLD ? next.slice(-CONVERSATION_UNLOAD_THRESHOLD) : next;
      });
    });
    const unsubscribeLive = uiLive?.subscribe?.((frame) => {
      if (!active) return;
      if (frame === "disconnected") return;
      if (frame === "connected") { void refresh(); return; }
      if (frame.type !== "history_entry" || frame.view !== "complete" || frame.entry.agent_id !== peer) return;
      setActivityEntries((entries) => [...entries.filter((entry) => entry.id !== frame.entry.id), frame.entry].sort(compareManagedHistoryEntries).slice(-CONVERSATION_UNLOAD_THRESHOLD));
      if (frame.entry.presentation?.kind === "message" || frame.entry.message === "Assistant response" || frame.entry.category === "assistant") {
        void refresh();
      }
    });
    void refresh();
    return () => {
      active = false;
      requestGeneration.current += 1;
      // The invalidated request cannot release the lock or run its queued repair.
      refreshing.current = false;
      repairQueued.current = false;
      unsubscribe();
      unsubscribeLive?.();
    };
  }, [transport, scopeKey, scope.routerUrl, scope.agentId, peer, refresh, uiLive]);

  const discardBefore = useCallback((visible: ReadonlySet<string>) => {
    const window = timelineWindowRef.current;
    const removed = window.events.filter((event) => event.seq > 0 && !visible.has(event.id) && !visible.has(event.id.replace(`msg:${peer}:`, "")) && !liveTimeline.ids.has(event.id));
    if (!removed.length) return;
    windowGeneration.current += 1;
    discardedCursor.current = true;
    olderRequest.current = null;
    setLoadingOlder(false);
    const ids = new Set(removed.map((event) => event.id));
    const next = { events: window.events.filter((event) => !ids.has(event.id)), trimmed: window.trimmed };
    timelineWindowRef.current = next;
    setTimelineWindow(next);
    setMessages((current) => current.filter((message) => !ids.has(message.id) && !ids.has(`msg:${peer}:${message.id}`)));
    setNextBeforeSeq((previous) => Math.max(previous ?? 0, ...removed.map((event) => event.seq + 1)));
  }, [peer, liveTimeline]);

  const send = useCallback(async (message: unknown, signal?: AbortSignal): Promise<Record<string, unknown>> => transport.sendMessage(message, signal), [transport]);
  const scopedError = messagesScopeKey === scopeKey ? error : undefined;
  const hasOlder = timelineWindow.trimmed || (nextBeforeSeq !== null && nextBeforeSeq !== undefined && nextBeforeSeq > 0)
    || (nextBefore !== null && nextBefore !== undefined && nextBefore > 0);
  return {
    messages: messagesScopeKey === scopeKey ? messages : [],
    timelineEvents: messagesScopeKey === scopeKey ? timelineEvents : [],
    liveTimelineIds: liveTimeline.key === scopeKey ? liveTimeline.ids : new Set(),
    events: messagesScopeKey === scopeKey ? timelineEvents : [],
    activityEntries: messagesScopeKey === scopeKey ? activityEntries : [],
    loading: messagesScopeKey !== scopeKey ? Boolean(peer) : effectiveLoading,
    loadingOlder: messagesScopeKey === scopeKey && loadingOlder,
    hasOlder: messagesScopeKey === scopeKey && hasOlder,
    ...(scopedError === undefined ? {} : { error: scopedError }),
    discardBefore,
    refresh,
    loadOlder,
    send,
  };
}
