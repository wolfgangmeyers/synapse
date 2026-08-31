import { normalizeRouterUrl } from "../persistence/settings-repository";
import type { HistoryMessage, MessageAttachment } from "../domain/history";
import type { ConversationMessage } from "../protocol/types";

const attachmentPath = /^\/v1\/attachments\/[A-Za-z0-9._~%-]+$/;

export function resolveAttachmentUrl(routerUrl: string, reference: string): string {
  if (!attachmentPath.test(reference)) return "";
  try { return new URL(reference, `${normalizeRouterUrl(routerUrl)}/`).toString(); } catch { return ""; }
}

export function absoluteAttachmentUrl(routerUrl: string, reference: string): string {
  if (reference.startsWith("/")) return resolveAttachmentUrl(routerUrl, reference);
  return "";
}

export function attachmentUrlMap(routerUrl: string, attachments: readonly MessageAttachment[]): MessageAttachment[] {
  return attachments.flatMap((attachment) => {
    if (typeof attachment.url !== "string") return [attachment];
    const url = absoluteAttachmentUrl(routerUrl, attachment.url);
    return url === "" ? [] : [{ ...attachment, url }];
  });
}

export function withAbsoluteAttachmentUrls<T extends { id: string; attachments?: readonly MessageAttachment[] }>(value: T, routerUrl: string): T {
  if (!Array.isArray(value.attachments)) return value;
  return { ...value, attachments: attachmentUrlMap(routerUrl, value.attachments) };
}

export function attachmentReferencesForConversation(routerUrl: string, message: ConversationMessage): ConversationMessage {
  return withAbsoluteAttachmentUrls(message, routerUrl);
}

export function attachmentReferencesForHistory(routerUrl: string, message: HistoryMessage): HistoryMessage {
  return withAbsoluteAttachmentUrls(message, routerUrl);
}
