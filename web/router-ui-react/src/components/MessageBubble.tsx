import type { ReactElement } from "react";
import type { HistoryMessage } from "../domain/history";
import { messageTime } from "../domain/history";
import { MarkdownBody } from "../domain/markdown";
import { AttachmentGallery } from "./AttachmentGallery";
import { attachmentReferencesFromBody } from "../domain/images";
import { textWithoutMarkdownImages } from "../domain/markdown";

export interface MessageBubbleProps { message: HistoryMessage; ownerId?: string | undefined; routerUrl?: string | undefined }
export function MessageBubble({ message, ownerId, routerUrl }: MessageBubbleProps): ReactElement {
  const outgoing = message.direction === "outgoing" || (ownerId !== undefined && message.from === ownerId);
  const attachments = message.attachments && message.attachments.length > 0
    ? message.attachments
    : routerUrl === undefined ? [] : attachmentReferencesFromBody(textWithoutMarkdownImages(message.body), routerUrl);
  const time = messageTime(message);
  return <article className={`bubble-wrap ${outgoing ? "outgoing" : "incoming"}`} data-message-id={message.id}><div className="bubble-label"><strong>{outgoing ? "You" : message.from ?? "Agent"}</strong><time dateTime={new Date(time).toISOString()}>{new Date(time).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}</time>{message.pending ? <span>Sending…</span> : message.failed ? <span className="error">Failed</span> : null}</div><div className="bubble"><MarkdownBody body={message.body ?? ""} /><AttachmentGallery attachments={attachments} {...(routerUrl !== undefined ? { routerUrl } : {})} /></div></article>;
}
