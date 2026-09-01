import { useLayoutEffect, useRef, type ReactElement } from "react";
import { messageKey, type HistoryMessage } from "../domain/history";
import { shouldFollowToBottom } from "../state/conversation-projection";
import { EmptyState } from "./EmptyState";
import { MessageBubble } from "./MessageBubble";

export interface MessageListProps { messages: HistoryMessage[]; peerId?: string | undefined; ownerId?: string | undefined; routerUrl?: string | undefined; loading?: boolean | undefined; error?: string | undefined; onNearBottomChange?: ((nearBottom: boolean) => void) | undefined }
export function MessageList({ messages, peerId, ownerId, routerUrl, loading = false, error, onNearBottomChange }: MessageListProps): ReactElement {
  const ref = useRef<HTMLDivElement>(null); const conversationKey = `${routerUrl ?? ""}\u001f${peerId ?? ""}`;
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
    const frame = requestAnimationFrame(() => { element.scrollTop = element.scrollHeight; });
    return () => cancelAnimationFrame(frame);
  }, [conversationKey, messages.length]);
  const onScroll = (): void => { const element = ref.current; if (!element) return; onNearBottomChange?.(shouldFollowToBottom(element.scrollTop, element.clientHeight, element.scrollHeight)); };
  const hasMessages = messages.length > 0;
  return <div ref={ref} className="messages" onScroll={onScroll} aria-live="polite">{hasMessages ? messages.map((message) => <MessageBubble key={messageKey(message)} message={message} {...(ownerId !== undefined ? { ownerId } : {})} {...(routerUrl !== undefined ? { routerUrl } : {})} />) : loading ? <EmptyState title="Loading conversation…" /> : error ? <p className="error" role="alert">{error}</p> : <EmptyState title="No messages yet" description="Send a message to start the conversation." />}{hasMessages && error ? <p className="error" role="alert">{error}</p> : null}</div>;
}
