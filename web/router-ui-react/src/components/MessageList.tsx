import { useLayoutEffect, useMemo, useRef, type ReactElement } from "react";
import { messageKey, messageTime, type HistoryMessage } from "../domain/history";
import type { ManagedHistoryEntry } from "../protocol/types";
import { shouldFollowToBottom } from "../state/conversation-projection";
import { EmptyState } from "./EmptyState";
import { MessageBubble } from "./MessageBubble";

export interface MessageListProps {
  id?: string | undefined;
  messages: HistoryMessage[];
  activityEntries?: ManagedHistoryEntry[] | undefined;
  peerId?: string | undefined;
  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  loading?: boolean | undefined;
  error?: string | undefined;
  onNearBottomChange?: ((nearBottom: boolean) => void) | undefined;
}

type FeedItem = { type: "message"; id: string; timestamp: number; message: HistoryMessage } | { type: "activity"; id: string; timestamp: number; entry: ManagedHistoryEntry };

function activityTime(entry: ManagedHistoryEntry): number {
  const parsed = Date.parse(entry.at ?? "");
  return Number.isFinite(parsed) ? parsed : Number.MAX_SAFE_INTEGER;
}

function formatActivityTime(entry: ManagedHistoryEntry): string | undefined {
  const timestamp = activityTime(entry);
  return timestamp === Number.MAX_SAFE_INTEGER ? undefined : new Date(timestamp).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
}

export function MessageList({ id, messages, activityEntries, peerId, ownerId, routerUrl, loading = false, error, onNearBottomChange }: MessageListProps): ReactElement {
  const ref = useRef<HTMLDivElement>(null);
  const conversationKey = `${routerUrl ?? ""}\u001f${peerId ?? ""}`;
  const items = useMemo<FeedItem[]>(() => [
    ...messages.map((message) => ({ type: "message" as const, id: messageKey(message), timestamp: messageTime(message, Number.MAX_SAFE_INTEGER), message })),
    ...(activityEntries ?? []).map((entry) => ({ type: "activity" as const, id: entry.id, timestamp: activityTime(entry), entry })),
  ].sort((left, right) => left.timestamp - right.timestamp || left.type.localeCompare(right.type) || left.id.localeCompare(right.id)), [activityEntries, messages]);
  const latestItem = items.at(-1);
  const latestItemKey = latestItem === undefined ? undefined : `${latestItem.type}\u001f${latestItem.id}`;
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
    const frame = requestAnimationFrame(() => { element.scrollTop = element.scrollHeight; });
    return () => cancelAnimationFrame(frame);
  }, [conversationKey, latestItemKey]);
  const onScroll = (): void => { const element = ref.current; if (!element) return; onNearBottomChange?.(shouldFollowToBottom(element.scrollTop, element.clientHeight, element.scrollHeight)); };
  const hasItems = items.length > 0;
  const hasActivity = activityEntries !== undefined;
  return <div ref={ref} {...(id === undefined ? {} : { id, role: "tabpanel" })} className="messages" onScroll={onScroll} aria-live="polite">{hasItems ? items.map((item) => item.type === "message" ? <MessageBubble key={`message:${item.id}`} message={item.message} {...(ownerId !== undefined ? { ownerId } : {})} {...(routerUrl !== undefined ? { routerUrl } : {})} /> : <article key={`activity:${item.id}`} className="activity-entry" data-activity-id={item.id}><div className="activity-entry-label"><strong>Activity</strong>{item.entry.level ? <span>{item.entry.level}</span> : null}{item.entry.at ? <time dateTime={item.entry.at}>{formatActivityTime(item.entry)}</time> : null}</div><p>{item.entry.message ?? item.entry.id}</p></article>) : loading ? <EmptyState title="Loading conversation…" /> : error ? <p className="error" role="alert">{error}</p> : <EmptyState title={hasActivity ? "No messages or activity yet" : "No messages yet"} description={hasActivity ? "Messages and operational activity will appear here." : "Send a message to start the conversation."} />}{hasItems && error ? <p className="error" role="alert">{error}</p> : null}</div>;
}
