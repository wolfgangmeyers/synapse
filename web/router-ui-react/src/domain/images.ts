export const MAX_IMAGES = 3;
export const MAX_IMAGE_EDGE = 1600;
export const IMAGE_JPEG_QUALITY = 0.92;
export const MAX_IMAGE_BYTES = 768 * 1024;
export const MAX_TOTAL_IMAGE_BYTES = MAX_IMAGES * MAX_IMAGE_BYTES;
export const HISTORY_IMAGE_EDGE = 360;
export const HISTORY_IMAGE_QUALITY = 0.88;

export interface ImageInput { dataUrl: string; name?: string; sizeBytes: number; mimeType: string }
export interface ImageAttachment { url: string; name?: string; mime_type?: string; size_bytes?: number; id?: string; cacheKey?: string }

export function canAddImages(existing: ImageInput[], additional: number): boolean { return additional >= 0 && existing.length + additional <= MAX_IMAGES; }
export function pendingAttachmentText(images: Pick<ImageInput, "name">[]): string {
  return `Attached image${images.length === 1 ? "" : "s"}:\n${images.map((image, index) => `${index + 1}. ${image.name || "image.jpg"}`).join("\n")}`;
}
export function hasSendableContent(body: string, images: ImageInput[]): boolean { return body.trim() !== "" || images.length > 0; }
export function attachmentReferencesFromBody(body: string, routerUrl: string): ImageAttachment[] {
  const result: ImageAttachment[] = [];
  const pattern = /\/v1\/attachments\/([A-Za-z0-9._-]+\.(?:png|jpe?g|webp|gif))/gi;
  for (const match of body.matchAll(pattern)) {
    const path = match[0];
    const name = match[1];
    const url = resolveAttachmentUrl(routerUrl, path);
    if (name !== undefined && url) result.push({ url, name });
  }
  return result;
}
export function resolveAttachmentUrl(routerUrl: string, reference: string): string {
  if (!/^\/v1\/attachments\/[A-Za-z0-9._-]+$/.test(reference)) return "";
  try { return new URL(reference, routerUrl).toString(); } catch { return ""; }
}

export async function convertImage(file: Blob & { name?: string }, options: { createImageBitmap?: typeof globalThis.createImageBitmap; canvasFactory?: () => HTMLCanvasElement } = {}): Promise<ImageInput> {
  const createBitmap = options.createImageBitmap ?? globalThis.createImageBitmap;
  if (typeof createBitmap !== "function") throw new Error("Image conversion is unavailable");
  const bitmap = await createBitmap(file);
  const edge = Math.max(bitmap.width, bitmap.height);
  const scale = Math.min(1, MAX_IMAGE_EDGE / edge);
  const width = Math.max(1, Math.round(bitmap.width * scale));
  const height = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = options.canvasFactory?.() ?? document.createElement("canvas");
  canvas.width = width; canvas.height = height;
  const context = canvas.getContext("2d");
  if (!context) throw new Error("Image conversion is unavailable");
  context.fillStyle = "white"; context.fillRect(0, 0, width, height); context.drawImage(bitmap, 0, 0, width, height);
  bitmap.close?.();
  const dataUrl = canvas.toDataURL("image/jpeg", IMAGE_JPEG_QUALITY);
  const sizeBytes = dataUrlByteSize(dataUrl);
  if (sizeBytes > MAX_IMAGE_BYTES) throw new Error(`${file.name || "Image"} is larger than 768 KiB after conversion.`);
  return { dataUrl, name: file.name || "image.jpg", sizeBytes, mimeType: "image/jpeg" };
}

export function dataUrlByteSize(dataUrl: string): number {
  const comma = dataUrl.indexOf(",");
  if (comma < 0) return 0;
  const payload = dataUrl.slice(comma + 1);
  if (/;base64/i.test(dataUrl.slice(0, comma))) return Math.floor(payload.replace(/\s/g, "").length * 3 / 4) - (payload.endsWith("==") ? 2 : payload.endsWith("=") ? 1 : 0);
  try { return new TextEncoder().encode(decodeURIComponent(payload)).length; } catch { return 0; }
}

export function validateImageBatch(images: ImageInput[]): string | undefined {
  if (images.length > MAX_IMAGES) return "Attach up to three images.";
  if (images.some((image) => image.sizeBytes > MAX_IMAGE_BYTES)) return "Each image must be no larger than 768 KiB.";
  if (images.reduce((total, image) => total + image.sizeBytes, 0) > MAX_TOTAL_IMAGE_BYTES) return "The images together must be no larger than 2304 KiB.";
  return undefined;
}

export async function createHistoryThumbnail(dataUrl: string, options: { createImageBitmap?: typeof globalThis.createImageBitmap; canvasFactory?: () => HTMLCanvasElement } = {}): Promise<Blob> {
  const createBitmap = options.createImageBitmap ?? globalThis.createImageBitmap;
  if (typeof createBitmap !== "function") throw new Error("Image thumbnailing is unavailable");
  const response = await fetch(dataUrl); const source = await response.blob(); const bitmap = await createBitmap(source);
  const scale = Math.min(1, HISTORY_IMAGE_EDGE / Math.max(bitmap.width, bitmap.height));
  const width = Math.max(1, Math.round(bitmap.width * scale)); const height = Math.max(1, Math.round(bitmap.height * scale));
  const canvas = options.canvasFactory?.() ?? document.createElement("canvas"); canvas.width = width; canvas.height = height;
  const context = canvas.getContext("2d"); if (!context) throw new Error("Image thumbnailing is unavailable");
  context.fillStyle = "white"; context.fillRect(0, 0, width, height); context.drawImage(bitmap, 0, 0, width, height); bitmap.close?.();
  const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", HISTORY_IMAGE_QUALITY));
  if (!blob) throw new Error("Image history preview could not be saved");
  return blob;
}
