import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { messageKey, messageTime, type HistoryMessage } from "../domain/history";
import type { ManagedHistoryEntry, ManagedHistoryPresentation, ManagedHistoryOutcome } from "../protocol/types";
import type { Agent } from "../state/agent-projection";
import { currentUsername } from "../persistence/settings-repository";
import { shouldFollowToBottom } from "../state/conversation-projection";
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
  peerId?: string | undefined;
  agent?: Agent | undefined;
  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  loading?: boolean | undefined;
  error?: string | undefined;
  onNearBottomChange?: ((nearBottom: boolean) => void) | undefined;
  scrollLocked?: boolean | undefined;
  onLoadOlder?: (() => void) | undefined;
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

function isMeaningfulCompleteActivity(entry: ManagedHistoryEntry, currentUser?: string, allEntries?: readonly ManagedHistoryEntry[]): boolean {
  if (isContextResetEntry(entry)) return true;
  if (isMailboxSendToUser(entry, currentUser, allEntries) !== undefined) return true;
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
  if (entry.presentation?.kind === "lifecycle") return false;
  if (entry.presentation?.kind === "state" && !entry.message?.toLowerCase().includes("workflow") && !entry.presentation?.summary?.toLowerCase().includes("workflow")) return false;
  const isOutboundMessage = entry.presentation?.kind === "message" && (
    entry.message === "Message sent" || entry.message === "Reply sent" ||
    entry.presentation?.from === entry.agent_id
  );
  if (isOutboundMessage) {
    const rawContent = entry.message_content ?? entry.presentation?.summary;
    const trimmed = rawContent?.trim();
    if (!trimmed || trimmed === "(Action completed without additional text)") {
      const toolCallsExist = allEntries
        ? allEntries.some((e) => e.presentation?.kind === "tool" || (e as { category?: unknown }).category === "managed_tool")
        : true;
      if (toolCallsExist) return false;
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
  const meta = event.metadata ?? {};
  const fromMeta = typeof meta.from === "string" ? meta.from : undefined;
  const toMeta = typeof meta.to === "string" ? meta.to : undefined;

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
    const rawAttachments = meta.attachments ?? (event as any).attachments ?? meta.images;
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
  const rawOutcome = (meta.presentation as any)?.outcome ?? meta.outcome;
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

export function MessageList({ id, messages, events, timelineEvents, activityEntries, activityHistoryReady = true, peerId, agent, ownerId, routerUrl, loading = false, error, onNearBottomChange, scrollLocked, onLoadOlder, hasOlder, loadingOlder }: MessageListProps): ReactElement {
  const currentUser = ownerId ?? currentUsername(undefined, routerUrl);
  const ref = useRef<HTMLDivElement>(null);
  const conversationKey = `${routerUrl ?? ""}\u001f${peerId ?? ""}`;
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
    fetchConversationTimeline(peerId, 50, undefined, routerUrl)
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
  const effectiveHasOlder = hasOlder ?? internalHasOlder;
  const effectiveLoadingOlder = loadingOlder ?? internalLoadingOlder;

  const prevScrollHeightRef = useRef<number | null>(null);
  const prevScrollTopRef = useRef<number | null>(null);
  const isPrependingRef = useRef(false);

  const handleInternalLoadOlder = useCallback(() => {
    const element = ref.current;
    if (element) {
      prevScrollHeightRef.current = element.scrollHeight;
      prevScrollTopRef.current = element.scrollTop;
      isPrependingRef.current = true;
    }
    if (onLoadOlder) {
      onLoadOlder();
      return;
    }
    if (!peerId || internalLoadingOlder) return;
    const oldestSeq = internalEvents[0]?.seq;
    if (!oldestSeq || oldestSeq <= 0) return;
    setInternalLoadingOlder(true);
    fetchConversationTimeline(peerId, 50, oldestSeq, routerUrl)
      .then((resp) => {
        setInternalEvents((prev) => [...resp.events, ...prev]);
        setInternalHasOlder(Boolean(resp.next_before_seq && resp.next_before_seq > 0));
      })
      .catch(() => undefined)
      .finally(() => {
        setInternalLoadingOlder(false);
      });
  }, [onLoadOlder, peerId, internalLoadingOlder, internalEvents, routerUrl]);

  const items = useMemo<FeedItem[]>(() => {
    const seenIds = new Set<string>();
    const markSeen = (id: string) => {
      seenIds.add(id);
      const raw = extractRawId(id);
      if (raw) seenIds.add(raw);
    };
    const rawItems: FeedItem[] = [];

    const isSeen = (id: string): boolean => {
      if (seenIds.has(id)) return true;
      const raw = extractRawId(id);
      return !!raw && seenIds.has(raw);
    };

    if (effectiveEvents && effectiveEvents.length > 0) {
      const seenEventIds = new Set<string>();
      for (const ev of effectiveEvents) {
        if (seenEventIds.has(ev.id)) continue;
        seenEventIds.add(ev.id);
        const converted = timelineEventToFeedItems(ev, currentUser, ownerId, peerId);
        for (const item of converted) {
          if (isSeen(item.id)) continue;
          markSeen(item.id);
          rawItems.push(item);
        }
      }
    }

    // Include live or pending messages (from WebSocket) not already captured
    for (const message of messages ?? []) {
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
      if (!isMeaningfulCompleteActivity(entry, currentUser, activityEntries)) continue;
      const userMail = isMailboxSendToUser(entry, currentUser, activityEntries);
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
  }, [effectiveEvents, messages, activityEntries, currentUser, ownerId, peerId]);
  const renderedItems = useMemo(() => {
    let lastThinking: string | undefined;
    let currentEpoch = 1;
    const result: Array<{ item: FeedItem; suppressThinking: boolean; epoch?: number | string }> = [];
    for (const item of items) {
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
  }, [items]);
  const latestItem = renderedItems.at(-1)?.item;
  const latestItemKey = latestItem === undefined ? undefined : `${latestItem.type}\u001f${latestItem.id}`;
  const oldestItem = renderedItems[0]?.item;
  const oldestItemKey = oldestItem === undefined ? undefined : `${oldestItem.type}\u001f${oldestItem.id}`;

  // Preserve scroll viewport position when prepending older history at the top,
  // preventing the conversation feed from jumping or snapping to 0.
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element || prevScrollHeightRef.current === null || prevScrollTopRef.current === null) return;
    const heightDelta = element.scrollHeight - prevScrollHeightRef.current;
    if (heightDelta > 0) {
      element.scrollTop = prevScrollTopRef.current + heightDelta;
    }
    prevScrollHeightRef.current = null;
    prevScrollTopRef.current = null;
    isPrependingRef.current = false;
  }, [oldestItemKey]);

  useLayoutEffect(() => {
    if (scrollLocked || isPrependingRef.current) return;
    const element = ref.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
    const frame = requestAnimationFrame(() => {
      if (isPrependingRef.current) return;
      element.scrollTop = element.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [conversationKey, latestItemKey, scrollLocked]);
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
    onNearBottomChange?.(shouldFollowToBottom(element.scrollTop, element.clientHeight, element.scrollHeight));
    const isScrollable = element.scrollHeight > element.clientHeight + 20;
    if (isScrollable && element.scrollTop < 100 && effectiveHasOlder && !effectiveLoadingOlder) {
      handleInternalLoadOlder();
    }
  };
  const hasItems = renderedItems.length > 0;
  const hasActivity = activityEntries !== undefined || (effectiveEvents !== undefined && effectiveEvents.some((e) => e.kind !== "message"));
  const effectiveLoading = loading || internalLoading;
  let previousDate: string | undefined;

  return (
    <div ref={ref} {...(id === undefined ? {} : { id, role: "tabpanel" })} className="messages" onScroll={onScroll}>
      {peerId === undefined ? (
        <EmptyState watermark title="Pick an agent" description="Select an agent from the sidebar to view activity and chat." />
      ) : hasItems ? (
        <ol className="complete-timeline" aria-label={hasActivity ? `Complete activity timeline for ${peerId ?? "selected agent"}` : "Conversation messages"}>
          <div className="timeline-rail" aria-hidden="true" />
          {effectiveHasOlder && (
            <li key="pagination:load-older" className="timeline-load-older-item">
              <button type="button" className="load-older-button" onClick={handleInternalLoadOlder} disabled={effectiveLoadingOlder}>
                {effectiveLoadingOlder ? "Loading older history…" : "Load older history"}
              </button>
            </li>
          )}
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
                  <li className="timeline-item timeline-item--context-reset">
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
                  <div className="timeline-content">
                    {item.type === "message" ? (
                      <MessageBubble
                        message={item.message}
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
        <p className="error" role="alert">{error}</p>
      ) : (
        <AgentWelcomeCard
          agentId={peerId}
          {...(agent !== undefined ? { agent } : {})}
        />
      )}
      {hasItems && error ? <p className="error" role="alert">{error}</p> : null}
      <p className="timeline-live-region" aria-live="polite" aria-atomic="true">{announcement}</p>
    </div>
  );
}
