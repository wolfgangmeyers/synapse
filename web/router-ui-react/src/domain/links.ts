import { messageTime, type HistoryMessage } from "./history";

export const LINKS_PER_AGENT_LIMIT = 200;
// Dismissals are a tombstone map, not user-visible content; bound it separately.
const DISMISSED_LIMIT = LINKS_PER_AGENT_LIMIT * 5;

export interface ConversationLink {
  url: string;
  label: string;
  time: number;
  /** Set for links extracted from a message; absent for manually added links. */
  messageId?: string;
}

// URL characters, allowing one level of balanced parentheses (e.g. Wikipedia titles).
const MARKDOWN_LINK = /\[([^\]\n]+)\]\(\s*(https?:\/\/(?:[^\s()<>[\]"'`]|\([^\s()<>]*\))+)[^)]*\)/gi;
const PLAIN_URL = /https?:\/\/(?:[^\s()<>[\]"'`]|\([^\s()<>]*\))+/gi;
// Sentence punctuation and markdown emphasis that commonly trails a URL in prose.
const TRAILING_PUNCTUATION = /[.,;:!?*_~]+$/;

/** Returns a canonical http(s) href, or undefined for anything else (e.g. javascript:). */
function normalizeUrl(raw: string): string | undefined {
  const trimmed = raw.trim().replace(TRAILING_PUNCTUATION, "");
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return undefined;
    return parsed.href;
  } catch {
    return undefined;
  }
}

/** Short human preview for a URL without a label: host plus a truncated path. */
export function previewUrl(url: string, max = 48): string {
  try {
    const parsed = new URL(url);
    const rest = `${parsed.pathname === "/" ? "" : parsed.pathname}${parsed.search}`;
    const text = `${parsed.host.replace(/^www\./, "")}${rest}`;
    return text.length > max ? `${text.slice(0, max - 1)}…` : text;
  } catch {
    return url.length > max ? `${url.slice(0, max - 1)}…` : url;
  }
}

function extractLinksFromText(body: string): { url: string; label: string }[] {
  const found: { url: string; label: string }[] = [];
  // Markdown links go first and are blanked out so the plain-URL pass below
  // does not extract their URLs a second time.
  const remainder = body.replace(MARKDOWN_LINK, (_match, label: string, rawUrl: string) => {
    const url = normalizeUrl(rawUrl);
    if (url !== undefined) found.push({ url, label: label.trim() || previewUrl(url) });
    return " ";
  });
  for (const match of remainder.matchAll(PLAIN_URL)) {
    const url = normalizeUrl(match[0]);
    if (url !== undefined) found.push({ url, label: previewUrl(url) });
  }
  return found;
}

/**
 * Extract de-duplicated links from messages, newest first. The most recent
 * mention decides the time and message; a bare repeat of a URL keeps the
 * label from an earlier markdown mention instead of degrading to a preview.
 */
export function extractLinks(messages: readonly HistoryMessage[]): ConversationLink[] {
  const byUrl = new Map<string, ConversationLink>();
  // Do not rely on caller ordering: history merges can interleave.
  const ordered = messages.map((message, index) => ({ message, index }))
    .sort((a, b) => messageTime(a.message, 0) - messageTime(b.message, 0) || a.index - b.index);
  for (const { message } of ordered) {
    if (typeof message.body !== "string" || message.pending || message.failed) continue;
    const time = messageTime(message, 0);
    for (const link of extractLinksFromText(message.body)) {
      const previous = byUrl.get(link.url);
      const label = previous !== undefined && link.label === previewUrl(link.url) ? previous.label : link.label;
      // Delete first so Map insertion order tracks recency.
      byUrl.delete(link.url);
      byUrl.set(link.url, { ...link, label, time, messageId: message.id });
    }
  }
  return [...byUrl.values()].reverse();
}

export interface LinksState {
  manual: ConversationLink[];
  /**
   * url -> id of the message occurrence the user deleted. Keyed by message id so
   * a newer message that mentions the URL again makes it reappear instead of
   * staying muted forever.
   */
  dismissed: Record<string, string>;
}

export const EMPTY_LINKS_STATE: LinksState = { manual: [], dismissed: {} };

/** Merge extracted and manual links, hide dismissed occurrences, newest first. */
export function visibleLinks(extracted: readonly ConversationLink[], state: LinksState): ConversationLink[] {
  const byUrl = new Map<string, ConversationLink>();
  for (const link of extracted) {
    if (link.messageId !== undefined && state.dismissed[link.url] === link.messageId) continue;
    byUrl.set(link.url, link);
  }
  // A manual entry wins over an extracted one only if it is at least as recent.
  for (const link of state.manual) {
    const existing = byUrl.get(link.url);
    if (existing === undefined || link.time >= existing.time) byUrl.set(link.url, link);
  }
  return [...byUrl.values()].sort((a, b) => b.time - a.time);
}

/** Returns undefined when the URL is not a valid http(s) URL. */
export function addManualLink(state: LinksState, rawUrl: string, rawLabel: string, now = Date.now()): LinksState | undefined {
  const url = normalizeUrl(rawUrl);
  if (url === undefined) return undefined;
  const label = rawLabel.trim() || previewUrl(url);
  const manual = [{ url, label, time: now }, ...state.manual.filter((link) => link.url !== url)]
    .slice(0, LINKS_PER_AGENT_LIMIT);
  // Adding a link the user previously deleted brings it back.
  const { [url]: _undismissed, ...dismissed } = state.dismissed;
  return { manual, dismissed };
}

function capDismissed(dismissed: Record<string, string>): Record<string, string> {
  const entries = Object.entries(dismissed);
  return entries.length <= DISMISSED_LIMIT ? dismissed : Object.fromEntries(entries.slice(-DISMISSED_LIMIT));
}

/**
 * Delete a link: drop any manual entry and dismiss the message occurrence of the
 * same URL (looked up in `extracted`, since a manual entry can shadow one).
 */
export function removeLink(state: LinksState, link: ConversationLink, extracted: readonly ConversationLink[]): LinksState {
  const messageId = link.messageId ?? extracted.find((entry) => entry.url === link.url)?.messageId;
  return {
    manual: state.manual.filter((entry) => entry.url !== link.url),
    dismissed: messageId === undefined ? state.dismissed : capDismissed({ ...state.dismissed, [link.url]: messageId }),
  };
}

export function clearLinks(state: LinksState, extracted: readonly ConversationLink[]): LinksState {
  const dismissed = { ...state.dismissed };
  // `extracted` is newest-first; insert oldest-first so the cap evicts the oldest dismissals.
  for (const link of [...extracted].reverse()) if (link.messageId !== undefined) dismissed[link.url] = link.messageId;
  return { manual: [], dismissed: capDismissed(dismissed) };
}
