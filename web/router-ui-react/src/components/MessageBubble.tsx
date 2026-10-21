import type { ReactElement } from "react";
import type { HistoryMessage } from "../domain/history";
import { messageTime } from "../domain/history";
import { MarkdownBody } from "../domain/markdown";
import { AttachmentGallery } from "./AttachmentGallery";
import { attachmentReferencesFromBody } from "../domain/images";
import { textWithoutMarkdownImages } from "../domain/markdown";

import { AgentAvatar } from "./AgentAvatar";

export interface MessageBubbleProps { message: HistoryMessage; ownerId?: string | undefined; routerUrl?: string | undefined; operationalAgentId?: string | undefined }
export function MessageBubble({ message, ownerId, routerUrl, operationalAgentId }: MessageBubbleProps): ReactElement {
  const outgoing = message.direction === "outgoing" || (ownerId !== undefined && message.from === ownerId);
  const receivedBySelectedAgent = operationalAgentId !== undefined && message.to === operationalAgentId;
  const sentBySelectedAgent = operationalAgentId !== undefined && message.from === operationalAgentId;
  const communicationLabel = outgoing ? null : receivedBySelectedAgent ? "Received message" : sentBySelectedAgent ? "Sent message" : undefined;
  const attachments = message.attachments && message.attachments.length > 0
    ? message.attachments
    : routerUrl === undefined ? [] : attachmentReferencesFromBody(textWithoutMarkdownImages(message.body), routerUrl);
  const time = messageTime(message);
  return (
    <article className={`bubble-wrap ${outgoing ? "outgoing" : "incoming"}`} data-message-id={message.id}>
      <div className="bubble-row">
        <div className={`message-avatar ${outgoing ? "message-avatar--user" : "message-avatar--agent"}`} aria-hidden="true">
          {outgoing ? (
            <span className="user-avatar-circle" title="You">
              <i className="fa-solid fa-user" aria-hidden="true" />
            </span>
          ) : (
            <span className="agent-avatar-circle" title={message.from ?? "Agent"}>
              <AgentAvatar agentId={message.from ?? operationalAgentId ?? "agent"} />
            </span>
          )}
        </div>
        <div className="bubble-main">
          <div className="bubble-label">
            {outgoing ? (
              <span className="message-transport message-transport--sent">
                <i className="fa-solid fa-paper-plane" aria-hidden="true" />
              </span>
            ) : communicationLabel ? (
              <span className={`message-transport message-transport--${receivedBySelectedAgent ? "received" : "sent"}`}>
                <i className={`fa-solid ${receivedBySelectedAgent ? "fa-envelope" : "fa-paper-plane"}`} aria-hidden="true" />
                {communicationLabel}
              </span>
            ) : null}
            <strong>{outgoing ? "You" : message.from ?? "Agent"}</strong>
            <time dateTime={new Date(time).toISOString()}>
              {new Date(time).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}
            </time>
            {message.pending ? <span>Sending…</span> : message.failed ? <span className="error">Failed</span> : null}
          </div>
          <div className="bubble">
            <MarkdownBody body={message.body ?? ""} />
            <AttachmentGallery attachments={attachments} {...(routerUrl !== undefined ? { routerUrl } : {})} />
          </div>
        </div>
      </div>
    </article>
  );
}
