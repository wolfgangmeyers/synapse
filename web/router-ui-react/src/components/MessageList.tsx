import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { DEFAULT_CONVERSATION_CHUNK_LIMIT, messageKey, messageTime, type HistoryMessage } from "../domain/history";
import type { ManagedHistoryEntry, ManagedHistoryPresentation, ManagedHistoryOutcome } from "../protocol/types";
import type { Agent } from "../state/agent-projection";
import { currentUsername } from "../persistence/settings-repository";
import { DEFAULT_AUTO_SCROLL_THRESHOLD, shouldFollowToBottom } from "../state/conversation-projection";
import { fetchConversationTimeline, type ConversationTimelineEvent } from "../services/api";
import { AgentWelcomeCard } from "./AgentWelcomeCard";
import { CompleteTimelineEntry, completeTimelineVisual, extractEpoch, getThinkingContent, isActiveManagedSubagentLifecycle, isContextResetEntry, isMailboxSendToUser } from "./CompleteTimelineEntry";
import { EmptyState } from "./EmptyState";
import { MessageBubble } from "./MessageBubble";

export interface MessageListProps {
  id?: string | undefined;
  messages?: HistoryMessage[];
  events?: ConversationTimelineEvent[] | undefined;
  timelineEvents?: ConversationTimelineEvent[] | undefined;
  activityEntries?: ManagedHistoryEntry[] | undefined;
  activityHistoryReady?: boolean | undefined;
  liveActivityIds?: ReadonlySet<string> | undefined;
  liveTimelineIds?: ReadonlySet<string> | undefined;
  historyWindowReady?: boolean | undefined;
  onDiscardHistoryBefore?: ((ids: ReadonlySet<string>) => void) | undefined;
  peerId?: string | undefined;
  agent?: Agent | undefined;
  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  loading?: boolean | undefined;
  error?: string | undefined;
  onRetryHistory?: (() => void) | undefined;
  onNearBottomChange?: ((nearBottom: boolean) => void) | undefined;
  onLoadOlder?: ((beforeCommit?: () => void) => Promise<void>) | undefined;
  hasOlder?: boolean | undefined;
  loadingOlder?: boolean | undefined;
}

export function TimelineLoadingSkeleton(): ReactElement {
  return (
    <div
      className="timeline-loading-skeleton"
      aria-busy="true"
      aria-label="Loading conversation timeline"
    >
      <div className="skeleton-rail" aria-hidden="true" />
      <div className="skeleton-item skeleton-item--incoming">
        <div className="skeleton-dot" aria-hidden="true" />
        <div className="skeleton-bubble">
          <div className="skeleton-line skeleton-line--title" />
          <div className="skeleton-line skeleton-line--body" />
          <div className="skeleton-line skeleton-line--short" />
        </div>
      </div>
      <div className="skeleton-item skeleton-item--tool">
        <div className="skeleton-dot skeleton-dot--tool" aria-hidden="true" />
        <div className="skeleton-pill">
          <div className="skeleton-line skeleton-line--pill-text" />
        </div>
      </div>
      <div className="skeleton-item skeleton-item--outgoing">
        <div className="skeleton-bubble skeleton-bubble--outgoing">
          <div className="skeleton-line skeleton-line--body" />
        </div>
      </div>
      <div className="skeleton-item skeleton-item--incoming">
        <div className="skeleton-dot" aria-hidden="true" />
        <div className="skeleton-bubble">
          <div className="skeleton-line skeleton-line--body" />
          <div className="skeleton-line skeleton-line--short" />
        </div>
      </div>
    </div>
  );
}

type FeedItem = { type: "message"; id: string; timestamp: number; sequence: number; message: HistoryMessage } | { type: "activity"; id: string; timestamp: number; sequence: number; entry: ManagedHistoryEntry };

export type TimelineOrbTone = "purple" | "cyan" | "danger" | "blue";

export function getItemOrbTone(item: FeedItem, ownerId?: string): TimelineOrbTone {
  if (item.type === "message") {
    const outgoing = item.message.direction === "outgoing" || (ownerId !== undefined && item.message.from === ownerId);
    return outgoing ? "blue" : "purple";
  }
  const entry = item.entry;
  const presentation = entry.presentation;
  const isError = entry.level === "error" || presentation?.outcome === "error" || presentation?.outcome === "failed" || presentation?.outcome === "interrupted";
  if (isError) return "danger";
  if (presentation?.kind === "thinking" || entry.thinking) return "purple";
  if (presentation?.kind === "tool") return "cyan";
  if (presentation?.kind === "child" || presentation?.kind === "subagent" || presentation?.kind === "subagent_complete") return "cyan";
  if (presentation?.kind === "state" && (entry.message?.toLowerCase().includes("workflow") || presentation.summary?.toLowerCase().includes("workflow"))) return "purple";
  const visual = completeTimelineVisual(entry);
  if (visual.tone === "danger") return "danger";
  if (visual.tone === "purple") return "purple";
  if (visual.tone === "blue") return "blue";
  return "cyan";
}

function activityTime(entry: ManagedHistoryEntry): number {
  const parsed = Date.parse(entry.at ?? "");
  return Number.isFinite(parsed) ? parsed : Number.MAX_SAFE_INTEGER;
}

function activitySequence(entry: ManagedHistoryEntry): number {
  const match = /^complete:(\d+)$/.exec(entry.id);
  if (match === null) return Number.MAX_SAFE_INTEGER;
  const value = Number(match[1]);
  return Number.isSafeInteger(value) ? value : Number.MAX_SAFE_INTEGER;
}

function isMeaningfulCompleteActivity(entry: ManagedHistoryEntry, currentUser?: string, hasToolCalls: boolean = true): boolean {
  if (isContextResetEntry(entry)) return true;
  if (isMailboxSendToUser(entry, currentUser, hasToolCalls) !== undefined) return true;
  if (currentUser) {
    const from = (entry.presentation?.from ?? entry.sender_identity)?.trim().toLowerCase();
    const user = currentUser.trim().toLowerCase();
    if (from && (from === user || from === "user") && entry.presentation?.kind === "message" && entry.message !== "Message sent" && entry.message !== "Reply sent") {
      return false;
    }
  }
  // A managed child stays in the persistent active-subagents area until its
  // terminal update. Its actual subagent tool invocation remains a normal
  // Complete history entry because it has presentation.kind === "tool".
  if (isActiveManagedSubagentLifecycle(entry)) return false;
  // Complete is an outcome timeline, not a lifecycle progress log.
  // Background completion is an outcome, not lifecycle progress. The router's
  // history transport omits category, so identify this dedicated notice by its
  // lifecycle title, including on replay.
  if (entry.presentation?.kind === "lifecycle" && entry.message !== "Background task finished") return false;
  if (entry.presentation?.kind === "state" && !entry.message?.toLowerCase().includes("workflow") && !entry.presentation?.summary?.toLowerCase().includes("workflow")) return false;
  const isOutboundMessage = entry.presentation?.kind === "message" && (
    entry.message === "Message sent" || entry.message === "Reply sent" ||
    entry.presentation?.from === entry.agent_id
  );
  if (isOutboundMessage) {
    const rawContent = entry.message_content ?? entry.presentation?.summary;
    const trimmed = rawContent?.trim();
    if (!trimmed || trimmed === "(Action completed without additional text)") {
      if (hasToolCalls) return false;
    }
  }
  if (entry.presentation?.kind !== "child") return true;
  return ["completed", "steered", "aborted", "stopped", "error", "interrupted_unknown"].includes(entry.presentation.child_state ?? "");
}

function dateLabel(timestamp: number): string | undefined {
  if (timestamp === Number.MAX_SAFE_INTEGER) return undefined;
  return new Date(timestamp).toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

// Normalizes prefixed identifiers (e.g., 'msg:<peer>:<id>', 'hist:complete:<id>')
// to their underlying raw message/activity ID so timeline events and live WS frames deduplicate cleanly.
function extractRawId(id: string): string {
  if (id.startsWith("msg:")) {
    const parts = id.split(":");
    return parts.length >= 3 ? parts.slice(2).join(":") : id;
  }
  if (id.startsWith("hist:")) {
    return id.replace(/^hist:(?:complete:)?/, "");
  }
  return id;
}

function normalizeMessageBodyForComparison(body: string | undefined): string {
  if (!body) return "";
  return body
    .replace(/(?:\r?\n\r?\n)?Attached images?:\s*(?:\d+\.\s*[^\r\n]+\s*)+$/gi, "")
    .trim();
}

function isHumanOrOperator(identity?: string, currentUser?: string, ownerId?: string): boolean {
  if (!identity) return false;
  const norm = identity.trim().toLowerCase();
  const humanAliases = new Set(
    [currentUser, ownerId, "wolfgang", "user"]
      .filter((x): x is string => typeof x === "string" && x.trim().length > 0)
      .map((x) => x.trim().toLowerCase())
  );
  return humanAliases.has(norm);
}

function messageToFeedItem(
  message: HistoryMessage,
  currentUser?: string,
  ownerId?: string,
  peerId?: string
): FeedItem {
  const from = message.from?.trim();
  const to = message.to?.trim();
  const normalizedFrom = from?.toLowerCase();
  const isFromHuman = isHumanOrOperator(from, currentUser, ownerId);
  const isToHuman = isHumanOrOperator(to, currentUser, ownerId);

  // System or router automated messages always render as envelope activity cards
  const isSystemOrRouterSender = normalizedFrom === "system" || normalizedFrom === "router";

  // An inter-agent message is strictly between two non-human agents (neither from nor to human)
  const isInterAgentSender = Boolean(
    from &&
    to &&
    !isFromHuman &&
    !isToHuman
  );

  if (isSystemOrRouterSender || isInterAgentSender) {
    const entry: ManagedHistoryEntry = {
      id: message.id,
      agent_id: peerId ?? message.to ?? "agent",
      ...(message.created_at ? { at: message.created_at } : message.recorded_at ? { at: message.recorded_at } : {}),
      category: isSystemOrRouterSender ? "system" : "managed_message",
      level: "info",
      ...(message.body !== undefined ? { message: message.body, message_content: message.body, body: message.body } : {}),
      presentation: {
        kind: "mailbox_message",
        sender: from ?? "system",
        recipient: to ?? peerId ?? "agent",
        ...(typeof message.subject === "string" ? { subject: message.subject } : {}),
        outcome: "success",
      },
    };
    return {
      type: "activity",
      id: message.id,
      timestamp: messageTime(message, Number.MAX_SAFE_INTEGER),
      sequence: message.sequence ?? Number.MAX_SAFE_INTEGER,
      entry,
    };
  }

  return {
    type: "message",
    id: message.id,
    timestamp: messageTime(message, Number.MAX_SAFE_INTEGER),
    sequence: message.sequence ?? Number.MAX_SAFE_INTEGER,
    message,
  };
}

function timelineEventToFeedItems(
  event: ConversationTimelineEvent,
  currentUser?: string,
  ownerId?: string,
  peerId?: string
): FeedItem[] {
  const meta = (event.metadata && typeof event.metadata === "object") ? event.metadata as Record<string, unknown> : {};
  // Dual normalization: resolve sender and recipient from from/sender and to/recipient properties
  const fromMeta = typeof meta.from === "string" ? meta.from : (typeof meta.sender === "string" ? meta.sender : undefined);
  const toMeta = typeof meta.to === "string" ? meta.to : (typeof meta.recipient === "string" ? meta.recipient : undefined);

  // 1. Subagent completion notifications / system messages / router messages:
  // If sender is system or router, or content has [Direct message from system] / [Subagent Completed],
  // NEVER render as a user chat bubble! Render as mailbox envelope card!
  if (
    fromMeta === "system" ||
    fromMeta === "router" ||
    event.content?.startsWith("[Direct message from system]") ||
    event.content?.startsWith("[Subagent Completed]")
  ) {
    const entry: ManagedHistoryEntry = {
      id: event.id,
      agent_id: event.agent_id,
      at: new Date(event.created_at).toISOString(),
      category: "system",
      level: "info",
      ...(event.content !== undefined ? { message: event.content } : {}),
      presentation: {
        kind: "mailbox_message",
        sender: fromMeta ?? "system",
        recipient: toMeta ?? peerId ?? event.agent_id,
        ...(typeof meta.subject === "string" ? { subject: meta.subject } : {}),
        outcome: "success",
      },
    };
    return [{
      type: "activity",
      id: event.id,
      timestamp: event.created_at,
      sequence: event.seq,
      entry,
    }];
  }

  // 2. Messages
  if (event.kind === "message") {
    const normalizedFrom = fromMeta?.trim().toLowerCase();
    const isFromHuman = isHumanOrOperator(fromMeta, currentUser, ownerId);
    const isToHuman = isHumanOrOperator(toMeta, currentUser, ownerId);

    // Automated system/router messages render as mailbox envelope cards
    const isSystemOrRouterSender = normalizedFrom === "system" || normalizedFrom === "router";

    // Inter-agent mailbox messages are between two non-human agents (neither from nor to human)
    // If toMeta is explicitly another agent (!isToHuman && toMeta !== undefined), it's inter-agent mail!
    const isInterAgent = Boolean(
      fromMeta &&
      toMeta &&
      !isFromHuman &&
      !isToHuman
    );

    if (isSystemOrRouterSender || isInterAgent) {
      const entry: ManagedHistoryEntry = {
        id: event.id,
        agent_id: event.agent_id,
        at: new Date(event.created_at).toISOString(),
        category: isSystemOrRouterSender ? "system" : "managed_message",
        ...(event.content !== undefined ? { message: event.content, message_content: event.content, body: event.content } : {}),
        presentation: {
          kind: "mailbox_message",
          sender: fromMeta ?? "system",
          recipient: toMeta ?? peerId ?? event.agent_id,
          ...(typeof meta.subject === "string" ? { subject: meta.subject } : {}),
          outcome: "success",
        },
      };
      return [{
        type: "activity",
        id: event.id,
        timestamp: event.created_at,
        sequence: event.seq,
        entry,
      }];
    }

    // Direct operator <-> assistant conversation:
    // outgoing if from user / owner or role === "user"; incoming assistant bubble otherwise
    const isOutgoing = isFromHuman || (event.role === "user" && fromMeta === undefined);
    const from = fromMeta ?? (isOutgoing ? (ownerId ?? currentUser ?? "user") : (peerId ?? event.agent_id));
    const to = toMeta ?? (isOutgoing ? (peerId ?? event.agent_id) : (ownerId ?? currentUser ?? "user"));
    // Dual normalization: support attachments from metadata (preferred) or direct event property
    const rawAttachments = meta.attachments ?? (event as ConversationTimelineEvent & { attachments?: unknown }).attachments ?? meta.images;
    const message: HistoryMessage = {
      id: event.id,
      body: event.content ?? "",
      direction: isOutgoing ? "outgoing" : "incoming",
      from,
      to,
      sequence: event.seq,
      created_at: new Date(event.created_at).toISOString(),
      recorded_at: new Date(event.created_at).toISOString(),
      ...(Array.isArray(rawAttachments) ? { attachments: rawAttachments } : {}),
      ...meta,
    };
    return [{
      type: "message",
      id: event.id,
      timestamp: event.created_at,
      sequence: event.seq,
      message,
    }];
  }

  // 3. Activity / Tools / Context Reset / Thinking
  const metaCategory = typeof meta.category === "string" ? meta.category : undefined;
  const metaLevel = typeof meta.level === "string" ? meta.level : undefined;
  const metaSender = typeof meta.sender === "string" ? meta.sender : fromMeta;
  const metaToolName = typeof meta.tool_name === "string" ? meta.tool_name : undefined;
  // Dual normalization: check outcome from nested presentation object or top-level metadata
  const presentationOutcome = meta.presentation && typeof meta.presentation === "object" && "outcome" in meta.presentation
    ? (meta.presentation as { outcome?: unknown }).outcome
    : undefined;
  const rawOutcome = presentationOutcome ?? meta.outcome;
  const metaOutcome = rawOutcome === "success" || rawOutcome === "failed" || rawOutcome === "error" || rawOutcome === "active" || rawOutcome === "interrupted" || rawOutcome === "completed"
    ? rawOutcome
    : undefined;
  const metaSummary = typeof meta.summary === "string" ? meta.summary : undefined;
  const metaThinking = typeof meta.thinking === "string" ? meta.thinking : undefined;

  let presentation: ManagedHistoryPresentation | undefined;
  if (event.kind === "context_reset" || meta.kind === "context_reset") {
    presentation = { kind: "context_reset" };
  } else if (event.kind === "tool" || meta.kind === "tool" || metaToolName !== undefined) {
    const isFinished = metaOutcome === "success" || metaOutcome === "completed" || metaOutcome === "failed" || metaOutcome === "error";
    presentation = {
      kind: "tool",
      tool_name: metaToolName ?? "tool",
      outcome: isFinished ? (metaOutcome === "failed" || metaOutcome === "error" ? "failed" : "success") : (metaOutcome ?? "success"),
      ...(event.content !== undefined ? { summary: event.content } : metaSummary !== undefined ? { summary: metaSummary } : {}),
    };
  }

  const entry: ManagedHistoryEntry = {
    id: event.id,
    agent_id: event.agent_id,
    at: new Date(event.created_at).toISOString(),
    ...(metaCategory !== undefined ? { category: metaCategory } : event.kind === "tool" ? { category: "managed_tool" } : {}),
    ...(metaLevel !== undefined ? { level: metaLevel } : { level: "info" }),
    ...(event.content !== undefined ? { message: event.content } : {}),
    ...(presentation !== undefined ? { presentation } : {}),
    ...(metaSender !== undefined ? { sender_identity: metaSender } : {}),
    ...(metaThinking !== undefined ? { thinking: metaThinking } : event.kind === "thinking" && event.content !== undefined ? { thinking: event.content } : {}),
  };

  const userMail = isMailboxSendToUser(entry, currentUser, undefined);
  if (userMail !== undefined) {
    const rawItems: FeedItem[] = [];
    // Thinking card splitting: Separate assistant thinking blocks from the accompanying message body
    // into independent activity cards preceding the message bubble.
    const thinkingText = getThinkingContent(entry);
    if (thinkingText) {
      rawItems.push({
        type: "activity",
        id: `${entry.id}:thinking`,
        timestamp: event.created_at - 1,
        sequence: event.seq,
        entry: {
          id: `${entry.id}:thinking`,
          agent_id: entry.agent_id,
          ...(entry.at !== undefined ? { at: entry.at } : {}),
          presentation: {
            kind: "thinking",
            outcome: "success",
            thinking: thinkingText,
          },
          thinking: thinkingText,
        },
      });
    }
    rawItems.push({
      type: "message",
      id: `activity-msg-${entry.id}`,
      timestamp: event.created_at,
      sequence: event.seq,
      message: {
        id: entry.id,
        from: userMail.from,
        to: userMail.recipient,
        body: userMail.body,
        direction: "incoming",
        ...(entry.at !== undefined ? { at: entry.at, created_at: entry.at } : {}),
      },
    });
    return rawItems;
  }

  return [{
    type: "activity",
    id: entry.id,
    timestamp: event.created_at,
    sequence: event.seq,
    entry,
  }];
}

// Checks whether a history activity entry is already represented in the messages list.
// Note: Formerly used fuzzy timestamp matching (±120s); now uses normalized content and identity comparison for exact deduplication.
function isAlreadyInMessages(entry: ManagedHistoryEntry, messages: HistoryMessage[]): boolean {
  if (isContextResetEntry(entry)) return false;
  if (messages.some((m) => m.id === entry.id)) return true;

  const rawContent = entry.message_content ?? entry.presentation?.summary;
  const content = normalizeMessageBodyForComparison(rawContent);
  if (!content) return false;

  const entryFrom = entry.sender_identity ?? entry.presentation?.from ?? entry.agent_id;
  const entryTo = entry.presentation?.to;

  return messages.some((m) => {
    const mBody = normalizeMessageBodyForComparison(m.body);
    if (mBody !== content) return false;
    if (entryFrom && m.from && entryFrom.toLowerCase() !== m.from.toLowerCase()) {
      return false;
    }
    if (entryTo && m.to && entryTo.toLowerCase() !== m.to.toLowerCase()) {
      return false;
    }
    return true;
  });
}

export function MessageList({ id, messages, events, timelineEvents, activityEntries, activityHistoryReady = true, liveActivityIds, liveTimelineIds, historyWindowReady, onDiscardHistoryBefore, peerId, agent, ownerId, routerUrl, loading = false, error, onRetryHistory, onNearBottomChange, onLoadOlder, hasOlder, loadingOlder }: MessageListProps): ReactElement {
  const currentUser = ownerId ?? currentUsername(undefined, routerUrl);
  const ref = useRef<HTMLDivElement>(null);
  const conversationKey = JSON.stringify([routerUrl ?? "", ownerId ?? "", peerId ?? ""]);
  const isNearBottomRef = useRef(true);
  const prevConversationKeyRef = useRef<string | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const announcedActivity = useRef(new Set<string>());
  const activityLiveInitialized = useRef(false);

  const [internalEvents, setInternalEvents] = useState<ConversationTimelineEvent[]>([]);
  const [internalHasOlder, setInternalHasOlder] = useState(false);
  const [internalLoadingOlder, setInternalLoadingOlder] = useState(false);
  const [internalLoading, setInternalLoading] = useState(false);

  useEffect(() => {
    if (timelineEvents !== undefined || events !== undefined || (messages !== undefined && messages.length > 0) || !peerId) {
      return;
    }
    let active = true;
    setInternalLoading(true);
    fetchConversationTimeline(peerId, DEFAULT_CONVERSATION_CHUNK_LIMIT, undefined, routerUrl)
      .then((resp) => {
        if (!active) return;
        setInternalEvents(resp.events);
        setInternalHasOlder(Boolean(resp.next_before_seq && resp.next_before_seq > 0));
      })
      .catch(() => undefined)
      .finally(() => {
        if (active) setInternalLoading(false);
      });
    return () => { active = false; };
  }, [peerId, routerUrl, timelineEvents, events, messages]);

  const effectiveEvents = timelineEvents ?? events ?? (internalEvents.length > 0 ? internalEvents : undefined);
  const sourceHasOlder = hasOlder ?? internalHasOlder;
  const [windowState, setWindowState] = useState<{ key: string; expanded: boolean; ready: boolean }>({ key: conversationKey, expanded: false, ready: false });
  const windowCurrent = windowState.key === conversationKey ? windowState : { key: conversationKey, expanded: false, ready: false };
  useLayoutEffect(() => {
    setWindowState((current) => current.key !== conversationKey
      ? { key: conversationKey, expanded: false, ready: historyWindowReady !== false }
      : historyWindowReady !== false && !current.ready ? { ...current, ready: true } : current);
  }, [conversationKey, historyWindowReady]);
  const effectiveLoadingOlder = loadingOlder ?? internalLoadingOlder;

  type PaginationRequest = {
    settled: boolean;
    anchor?: { element: HTMLElement; offset: number };
  };
  const paginationRef = useRef<PaginationRequest | null>(null);
  const [, renderPagination] = useState(0);
  const lastScrollTopRef = useRef(0);
  const frameRef = useRef<number | null>(null);
  const readingViewportRef = useRef<{
    firstItemKey: string | undefined;
    anchors: Array<{ element: HTMLElement; offset: number }>;
  } | null>(null);
  const captureReadingViewport = useCallback(() => {
    const element = ref.current;
    if (!element) return;
    const viewport = element.getBoundingClientRect();
    const anchors = Array.from(element.querySelectorAll<HTMLElement>("[data-scroll-anchor]"));
    readingViewportRef.current = {
      firstItemKey: anchors[0]?.dataset.scrollAnchor,
      anchors: anchors.flatMap((item) => {
        const rect = item.getBoundingClientRect();
        return rect.bottom > viewport.top && rect.top < viewport.bottom
          ? [{ element: item, offset: rect.top - viewport.top }] : [];
      }),
    };
  }, []);

  useLayoutEffect(() => {
    paginationRef.current = null;
    readingViewportRef.current = null;
    isNearBottomRef.current = true;
    setInternalEvents([]);
    setInternalHasOlder(false);
    setInternalLoadingOlder(false);
    setInternalLoading(false);
    return () => {
      paginationRef.current = null;
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
    };
  }, [conversationKey]);


  const items = useMemo<FeedItem[]>(() => {
    const seenIds = new Set<string>();
    const markSeen = (id: string) => {
      seenIds.add(id);
      const raw = extractRawId(id);
      if (raw) seenIds.add(raw);
    };
    const rawItems: FeedItem[] = [];
    const hasToolCalls = activityEntries?.some((entry) => entry.presentation?.kind === "tool" || entry.category === "managed_tool") ?? true;

    const isSeen = (id: string): boolean => {
      if (seenIds.has(id)) return true;
      const raw = extractRawId(id);
      return !!raw && seenIds.has(raw);
    };

    const liveActivityEventIds = new Set<string>();
    for (const entry of activityEntries ?? []) if (liveActivityIds?.has(entry.id)) {
      liveActivityEventIds.add(entry.id);
      liveActivityEventIds.add(`hist:${entry.id}`);
      liveActivityEventIds.add(`hist:complete:${entry.id}`);
    }
    if (effectiveEvents && effectiveEvents.length > 0) {
      const seenEventIds = new Set<string>();
      for (const ev of effectiveEvents) {
        if (seenEventIds.has(ev.id)) continue;
        // Only a live update can supersede the timeline snapshot.
        if (ev.kind !== "message" && liveActivityEventIds.has(ev.id)) continue;
        seenEventIds.add(ev.id);
        const converted = timelineEventToFeedItems(ev, currentUser, ownerId, peerId);
        for (const item of converted) {
          if (isSeen(item.id)) continue;
          markSeen(item.id);
          rawItems.push(item);
        }
      }
    }

    // Include live or pending messages (from WebSocket) not already captured.
    // When effectiveEvents is present and active, fallback messages should only include
    // pending / optimistic messages, or items that are newer than effectiveEvents[0].
    for (const message of messages ?? []) {
      if (effectiveEvents && effectiveEvents.length > 0) {
        const oldestEvent = effectiveEvents[0];
        const isPending = Boolean(message.pending);
        const isNewer = oldestEvent ? messageTime(message) >= oldestEvent.created_at : true;
        if (!isPending && !message.failed && !isNewer) {
          continue;
        }
      }
      if (seenIds.has(message.id) || seenIds.has(messageKey(message))) {
        continue;
      }
      markSeen(message.id);
      const item = messageToFeedItem(message, currentUser, ownerId, peerId);
      markSeen(item.id);
      rawItems.push(item);
    }

    // Include live activity entries (from WebSocket) not already captured
    for (const entry of activityEntries ?? []) {
      if (seenIds.has(entry.id) || seenIds.has(`hist:${entry.id}`) || seenIds.has(`hist:complete:${entry.id}`)) continue;
      if (!isMeaningfulCompleteActivity(entry, currentUser, hasToolCalls)) continue;
      const userMail = isMailboxSendToUser(entry, currentUser, hasToolCalls);
      if (userMail !== undefined) {
        if (isAlreadyInMessages(entry, messages ?? [])) continue;
        const thinkingText = getThinkingContent(entry);
        if (thinkingText) {
          const thinkingId = `${entry.id}:thinking`;
          if (!seenIds.has(thinkingId)) {
            markSeen(thinkingId);
            rawItems.push({
              type: "activity" as const,
              id: thinkingId,
              timestamp: activityTime(entry) - 1,
              sequence: activitySequence(entry),
              entry: {
                id: thinkingId,
                agent_id: entry.agent_id,
                ...(entry.at !== undefined ? { at: entry.at } : {}),
                presentation: { kind: "thinking", outcome: "success", thinking: thinkingText },
                thinking: thinkingText,
              },
            });
          }
        }
        const msgId = `activity-msg-${entry.id}`;
        if (!seenIds.has(msgId) && !seenIds.has(entry.id)) {
          markSeen(msgId);
          markSeen(entry.id);
          rawItems.push({
            type: "message" as const,
            id: msgId,
            timestamp: activityTime(entry),
            sequence: activitySequence(entry),
            message: {
              id: entry.id,
              from: userMail.from,
              to: userMail.recipient,
              body: userMail.body,
              direction: "incoming",
              ...(entry.at !== undefined ? { recorded_at: entry.at, created_at: entry.at } : {}),
              ...(entry.presentation?.outcome === "failed" || entry.presentation?.outcome === "error" ? { failed: true } : {}),
              ...(entry.presentation?.outcome === "active" ? { pending: true } : {}),
            },
          });
        }
        continue;
      }
      if (isAlreadyInMessages(entry, messages ?? [])) continue;
      markSeen(entry.id);
      rawItems.push({
        type: "activity" as const,
        id: entry.id,
        timestamp: activityTime(entry),
        sequence: activitySequence(entry),
        entry,
      });
    }

    return rawItems.sort(
      (left, right) =>
        left.timestamp - right.timestamp ||
        left.sequence - right.sequence ||
        left.id.localeCompare(right.id)
    );
  }, [effectiveEvents, messages, activityEntries, liveActivityIds, loading, internalLoading, currentUser, ownerId, peerId]);
  // Compute the combined budget before releasing discarded source candidates.
  const visibleItems = useMemo(() => {
    if (historyWindowReady === undefined) return { items, deferred: false };
    const protectedIds = new Set<string>();
    for (const id of liveTimelineIds ?? []) { protectedIds.add(id); protectedIds.add(extractRawId(id)); }
    for (const id of liveActivityIds ?? []) { protectedIds.add(id); protectedIds.add(extractRawId(id)); protectedIds.add(`${id}:thinking`); protectedIds.add(`activity-msg-${id}`); }
    for (const message of messages ?? []) if (message.pending || message.failed) {
      protectedIds.add(message.id); protectedIds.add(extractRawId(message.id));
    }
    const isProtected = (item: FeedItem) => protectedIds.has(item.id) || protectedIds.has(extractRawId(item.id))
      || (item.type === "message" && (Boolean(item.message.pending) || Boolean(item.message.failed) || protectedIds.has(item.message.id)))
      || (item.type === "activity" && protectedIds.has(item.entry.id));
    const historical = items.filter((item) => !isProtected(item));
    const allowance = windowCurrent.expanded ? historical.length : windowCurrent.ready ? DEFAULT_CONVERSATION_CHUNK_LIMIT : 0;
    const visibleHistory = new Set(historical.slice(-allowance || historical.length));
    if (allowance === 0) visibleHistory.clear();
    return { items: items.filter((item) => isProtected(item) || visibleHistory.has(item)), deferred: historical.length > allowance };
  }, [items, historyWindowReady, liveTimelineIds, liveActivityIds, messages, windowCurrent.expanded, windowCurrent.ready]);
  useLayoutEffect(() => {
    if (!onDiscardHistoryBefore || !windowCurrent.ready || windowCurrent.expanded) return;
    const ids = new Set<string>();
    for (const item of visibleItems.items) {
      ids.add(item.id); ids.add(extractRawId(item.id));
      if (item.type === "message") ids.add(item.message.id);
      if (item.type === "activity") { ids.add(item.entry.id); ids.add(item.entry.id.replace(/:thinking$/, "")); }
    }
    onDiscardHistoryBefore(ids);
  }, [onDiscardHistoryBefore, visibleItems, windowCurrent.ready, windowCurrent.expanded, liveTimelineIds, liveActivityIds]);
  const effectiveHasOlder = sourceHasOlder || (windowCurrent.ready && visibleItems.deferred);

  const renderedItems = useMemo(() => {
    let lastThinking: string | undefined;
    let currentEpoch = 1;
    const result: Array<{ item: FeedItem; suppressThinking: boolean; epoch?: number | string }> = [];
    for (const item of visibleItems.items) {
      if (item.type === "message") {
        lastThinking = undefined;
        result.push({ item, suppressThinking: false });
        continue;
      }
      if (isContextResetEntry(item.entry)) {
        currentEpoch += 1;
        const parsedEpoch = extractEpoch(item.entry, currentEpoch);
        if (typeof parsedEpoch === "number") {
          currentEpoch = Math.max(currentEpoch, parsedEpoch);
        }
        result.push({ item, suppressThinking: false, epoch: parsedEpoch });
        continue;
      }
      const isThinkingKind = item.entry.presentation?.kind === "thinking";
      const thinkingText = getThinkingContent(item.entry);
      if (thinkingText) {
        if (thinkingText === lastThinking) {
          if (isThinkingKind) {
            continue;
          }
          result.push({ item, suppressThinking: true });
          continue;
        }
        lastThinking = thinkingText;
      }
      result.push({ item, suppressThinking: false });
    }
    return result;
  }, [visibleItems]);
  const handleInternalLoadOlder = useCallback(async () => {
    if (paginationRef.current || effectiveLoadingOlder || !effectiveHasOlder) return;
    const oldestSeq = internalEvents[0]?.seq;
    if (!visibleItems.deferred && !onLoadOlder && (!peerId || !oldestSeq || oldestSeq <= 0)) return;
    const request: PaginationRequest = { settled: false };
    paginationRef.current = request;
    const beforeCommit = () => {
      if (paginationRef.current !== request) return;
      setWindowState({ key: conversationKey, expanded: true, ready: true });
      if (request.anchor) return;
      const element = ref.current;
      if (!element || isNearBottomRef.current) return;
      const top = element.getBoundingClientRect().top;
      const anchor = Array.from(element.querySelectorAll<HTMLElement>("[data-scroll-anchor]"))
        .find((item) => item.getBoundingClientRect().bottom > top);
      if (anchor) request.anchor = { element: anchor, offset: anchor.getBoundingClientRect().top - top };
    };
    try {
      if (visibleItems.deferred && !onDiscardHistoryBefore) {
        beforeCommit();
        setWindowState({ key: conversationKey, expanded: true, ready: true });
      } else if (onLoadOlder) {
        await onLoadOlder(beforeCommit);
      } else if (peerId && oldestSeq) {
        setInternalLoadingOlder(true);
        const resp = await fetchConversationTimeline(peerId, DEFAULT_CONVERSATION_CHUNK_LIMIT, oldestSeq, routerUrl);
        if (paginationRef.current !== request) return;
        beforeCommit();
        setInternalEvents((prev) => [...resp.events, ...prev]);
        setInternalHasOlder(Boolean(resp.next_before_seq && resp.next_before_seq > 0));
      }
    } catch {
      // History remains readable when pagination fails.
    } finally {
      if (paginationRef.current === request) {
        request.settled = true;
        setInternalLoadingOlder(false);
        // Cleanup happens in layout, after the response's state updates commit.
        renderPagination((version) => version + 1);
      }
    }
  }, [onLoadOlder, peerId, effectiveLoadingOlder, effectiveHasOlder, internalEvents, routerUrl, conversationKey, visibleItems.deferred]);

  // Consume a response anchor on its render even if all returned IDs already exist.
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const switched = prevConversationKeyRef.current !== conversationKey;
    prevConversationKeyRef.current = conversationKey;
    if (switched) isNearBottomRef.current = true;
    const request = paginationRef.current;
    const previousViewport = readingViewportRef.current;
    const remainingKeys = new Set(Array.from(element.querySelectorAll<HTMLElement>("[data-scroll-anchor]"),
      (item) => item.dataset.scrollAnchor));
    // Native anchoring is disabled here. Restore a surviving visible item when
    // retention removes the front, not when an append merely grows total height.
    const frontTrimmed = previousViewport?.firstItemKey !== undefined
      && !remainingKeys.has(previousViewport.firstItemKey);
    const anchor = request?.anchor ?? (frontTrimmed
      ? previousViewport?.anchors.find((item) => element.contains(item.element)) : undefined);
    if (!switched && anchor && !isNearBottomRef.current && element.contains(anchor.element)) {
      element.scrollTop += anchor.element.getBoundingClientRect().top - element.getBoundingClientRect().top - anchor.offset;
    }
    if (request && request.settled) {
      paginationRef.current = null;
    }
    if (isNearBottomRef.current) element.scrollTop = element.scrollHeight;
    lastScrollTopRef.current = element.scrollTop;
    captureReadingViewport();
  });

  // Observe actual layout, including same-ID streaming, images, and viewport resize.
  // Reattach after empty/loading views become a timeline.
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const key = conversationKey;
    const align = () => {
      captureReadingViewport();
      if (!isNearBottomRef.current || paginationRef.current?.anchor) return;
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
      frameRef.current = requestAnimationFrame(() => {
        frameRef.current = null;
        if (prevConversationKeyRef.current !== key || !isNearBottomRef.current || paginationRef.current?.anchor) return;
        element.scrollTop = element.scrollHeight;
        lastScrollTopRef.current = element.scrollTop;
      });
    };
    align();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(align);
    observer?.observe(element);
    const timeline = element.querySelector(".complete-timeline");
    if (timeline) observer?.observe(timeline);
    return () => {
      observer?.disconnect();
      if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
      frameRef.current = null;
    };
  });
  // Retained history belongs to the newly selected agent; it is not a live
  // announcement just because this component was already mounted for another.
  useEffect(() => {
    activityLiveInitialized.current = false;
    announcedActivity.current.clear();
    setAnnouncement("");
  }, [conversationKey]);
  useEffect(() => {
    // The selected agent's retained page may arrive after this component. Do
    // not announce that replay as if every old error just happened.
    if (!activityHistoryReady) return;
    const activities = renderedItems.filter((r): r is { item: Extract<FeedItem, { type: "activity" }>; suppressThinking: boolean } => r.item.type === "activity").map((r) => r.item);
    if (!activityLiveInitialized.current) {
      for (const activity of activities) announcedActivity.current.add(activity.id);
      activityLiveInitialized.current = true;
      return;
    }
    const newHighValue = activities.filter((activity) => !announcedActivity.current.has(activity.id) && completeTimelineVisual(activity.entry).isHighValue);
    for (const activity of activities) announcedActivity.current.add(activity.id);
    setAnnouncement(newHighValue.slice(-3).map((activity) => completeTimelineVisual(activity.entry).label).join(". "));
  }, [activityHistoryReady, renderedItems]);
  const onScroll = (): void => {
    const element = ref.current;
    if (!element) return;
    const { scrollTop, scrollHeight, clientHeight } = element;
    // Layout notifications at the same position are not a change of reading intent.
    if (scrollTop === lastScrollTopRef.current) return;
    lastScrollTopRef.current = scrollTop;
    captureReadingViewport();
    const nearBottom = shouldFollowToBottom({ scrollTop, scrollHeight, clientHeight }, DEFAULT_AUTO_SCROLL_THRESHOLD);
    isNearBottomRef.current = nearBottom;
    onNearBottomChange?.(nearBottom);
    // A user movement between response and layout supersedes the captured offset.
    const anchor = paginationRef.current?.anchor;
    if (anchor && element.contains(anchor.element)) {
      anchor.offset = anchor.element.getBoundingClientRect().top - element.getBoundingClientRect().top;
    }
    if (scrollTop < 100 && effectiveHasOlder && !effectiveLoadingOlder && !error) {
      void handleInternalLoadOlder();
    }
  };

  // If the conversation is not scrollable (e.g. empty or too few items to fill viewport)
  // but older history is available, automatically load older history so the user sees history.
  // Note: Only trigger if historyWindowReady is not false, and not when visible items are deferred
  // within a ready bounded window (deferred items are revealed by scrolling).
  useEffect(() => {
    if (!effectiveHasOlder || effectiveLoadingOlder || error) return;
    if (historyWindowReady === false || visibleItems.deferred) return;
    const element = ref.current;
    if (!element) return;
    const isScrollable = element.scrollHeight > element.clientHeight + 20;
    if (!isScrollable) {
      void handleInternalLoadOlder();
    }
  }, [effectiveHasOlder, effectiveLoadingOlder, error, historyWindowReady, visibleItems.deferred, renderedItems.length, handleInternalLoadOlder]);
  const hasItems = renderedItems.length > 0;
  const hasActivity = activityEntries !== undefined || (effectiveEvents !== undefined && effectiveEvents.some((e) => e.kind !== "message"));
  const effectiveLoading = loading || internalLoading;
  let previousDate: string | undefined;

  return (
    <div ref={ref} {...(id === undefined ? {} : { id, role: "tabpanel" })} className="messages" onScroll={onScroll}>
      {peerId === undefined ? (
        <EmptyState watermark title="Pick an agent" description="Select an agent from the sidebar to view activity and chat." />
      ) : hasItems || effectiveHasOlder ? (
        <ol className="complete-timeline" aria-label={hasActivity ? `Complete activity timeline for ${peerId ?? "selected agent"}` : "Conversation messages"}>
          <div className="timeline-rail" aria-hidden="true" />
          {renderedItems.map(({ item, suppressThinking, epoch }) => {
            const itemKey = `${item.type}:${item.id}`;
            const currentDate = dateLabel(item.timestamp);
            const separator = currentDate !== undefined && currentDate !== previousDate ? (
              <li key={`date:${currentDate}:${itemKey}`} className="timeline-date-item" role="separator" aria-label={`Date: ${currentDate}`}>
                <div className="timeline-date-divider">
                  <span className="timeline-date-line" aria-hidden="true" />
                  <span className="timeline-date-label">
                    <i className="fa-regular fa-calendar" aria-hidden="true" />
                    {currentDate}
                  </span>
                  <span className="timeline-date-line" aria-hidden="true" />
                </div>
              </li>
            ) : null;
            previousDate = currentDate ?? previousDate;

            const isContextReset = item.type === "activity" && isContextResetEntry(item.entry);
            if (isContextReset) {
              return (
                <Fragment key={itemKey}>
                  {separator}
                  <li className="timeline-item timeline-item--context-reset" data-scroll-anchor={itemKey}>
                    <CompleteTimelineEntry entry={item.entry} suppressThinking={suppressThinking} {...(epoch !== undefined ? { epoch } : {})} />
                  </li>
                </Fragment>
              );
            }

            const isOutgoing = item.type === "message" && (item.message.direction === "outgoing" || (ownerId !== undefined && item.message.from === ownerId));
            const orbTone = getItemOrbTone(item, ownerId);

            return (
              <Fragment key={itemKey}>
                {separator}
                <li className={`timeline-item timeline-item--${item.type}${isOutgoing ? " timeline-item--outgoing" : ""} timeline-item--tone-${orbTone}`}>
                  {!isOutgoing && (
                    <div className="timeline-node" aria-hidden="true">
                      <span className={`timeline-dot timeline-dot--${orbTone}`} />
                    </div>
                  )}
                  <div className="timeline-content" data-scroll-anchor={itemKey}>
                    {item.type === "message" ? (
                      <MessageBubble
                        message={item.message}
                        {...(peerId !== undefined ? { cachePeer: peerId } : {})}
                        {...(ownerId !== undefined ? { ownerId } : {})}
                        {...(routerUrl !== undefined ? { routerUrl } : {})}
                        {...(hasActivity && peerId !== undefined ? { operationalAgentId: peerId } : {})}
                      />
                    ) : (
                      <CompleteTimelineEntry
                        entry={item.entry}
                        suppressThinking={suppressThinking}
                        {...(epoch !== undefined ? { epoch } : {})}
                      />
                    )}
                  </div>
                </li>
              </Fragment>
            );
          })}
        </ol>
      ) : effectiveLoading ? (
        <TimelineLoadingSkeleton />
      ) : error ? (
        <div className="error" role="alert">{error} {onRetryHistory && <button type="button" onClick={onRetryHistory}>Retry history</button>}</div>
      ) : (
        <AgentWelcomeCard
          agentId={peerId}
          {...(agent !== undefined ? { agent } : {})}
        />
      )}
      {(hasItems || effectiveHasOlder) && error ? (
        <div className="error" role="alert">
          {error}{" "}
          <button type="button" onClick={() => { if (onRetryHistory) onRetryHistory(); else void handleInternalLoadOlder(); }}>
            Retry history
          </button>
        </div>
      ) : null}
      <p className="timeline-live-region" aria-live="polite" aria-atomic="true">{announcement}</p>
    </div>
  );
}
