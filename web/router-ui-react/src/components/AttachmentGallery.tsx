import { useEffect, useState, type ReactElement } from "react";
import type { MessageAttachment } from "../domain/history";
import { cachedImage } from "../persistence/history-repository";

export interface AttachmentGalleryProps { attachments?: MessageAttachment[] | undefined; routerUrl?: string | undefined }
// Resolves attachment URLs: leaves data/blob/http URLs intact, but prefixes
// server-relative router endpoints (/v1/attachments/...) with the active router base URL.
function urlFor(attachment: MessageAttachment, routerUrl?: string): string {
  const value = attachment.url ?? (attachment.id ? `/v1/attachments/${attachment.id}` : "");
  if (value.startsWith("data:")) return value;
  if (!value) return "";
  if (/^https?:\/\//.test(value)) return value;
  if (value.startsWith("/v1/attachments/")) {
    if (!routerUrl) return value;
    return `${routerUrl.replace(/\/$/, "")}${value}`;
  }
  return "";
}
export function AttachmentGallery({ attachments = [], routerUrl }: AttachmentGalleryProps): ReactElement | null {
  const [selected, setSelected] = useState<string>();
  const [cachedUrls, setCachedUrls] = useState<Record<string, string>>({});
  const cacheKeySignature = attachments.map((attachment) => attachment.cacheKey ?? "").join("\u001f");
  useEffect(() => {
    let active = true;
    const objectUrls: string[] = [];
    const keys = attachments.flatMap((attachment) => typeof attachment.cacheKey === "string" && attachment.cacheKey !== "" ? [attachment.cacheKey] : []);
    if (keys.length === 0) return () => undefined;
    const load = async (): Promise<void> => {
      const entries = await Promise.all(attachments.flatMap((attachment) => {
        const key = attachment.cacheKey;
        if (typeof key !== "string" || key === "") return [];
        return [cachedImage(key).then((record) => {
          if (record === undefined || typeof URL.createObjectURL !== "function") return undefined;
          const url = URL.createObjectURL(record.blob); objectUrls.push(url); return { key, url };
        }).catch(() => undefined)];
      }));
      if (!active) return;
      const next: Record<string, string> = {};
      for (const entry of entries) if (entry !== undefined) next[entry.key] = entry.url;
      setCachedUrls(next);
    };
    void load();
    return () => { active = false; for (const url of objectUrls) URL.revokeObjectURL(url); };
  }, [cacheKeySignature]);
  const usable = attachments.map((attachment) => {
    const cached = typeof attachment.cacheKey === "string" ? cachedUrls[attachment.cacheKey] : undefined;
    return { attachment, url: cached ?? urlFor(attachment, routerUrl) };
  }).filter((item): item is { attachment: MessageAttachment; url: string } => item.url.startsWith("data:") || /^https?:\/\//.test(item.url) || item.url.startsWith("blob:") || item.url.startsWith("/v1/attachments/"));
  if (usable.length === 0) return null;
  return <><div className="bubble-images" aria-label="Message images">{usable.slice(0, 3).map(({ attachment, url }, index) => <button type="button" key={`${url}-${index}`} className="attachment-image-button attachment-thumb-btn" onClick={() => setSelected(url)} aria-label={`Open ${attachment.name ?? `image ${index + 1}`}`}><img src={url} alt={attachment.name ?? `Attachment ${index + 1}`} /></button>)}</div>{selected ? <div className="image-lightbox" role="dialog" aria-label="Image preview" onClick={() => setSelected(undefined)}><img src={selected} alt="Full-size attachment" /><button type="button" className="image-lightbox-close" onClick={() => setSelected(undefined)} aria-label="Close image preview">×</button></div> : null}</>;
}
