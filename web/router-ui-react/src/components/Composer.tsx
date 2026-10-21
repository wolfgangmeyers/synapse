import { useEffect, useRef, useState, type ChangeEvent, type ClipboardEvent, type DragEvent, type FormEvent, type KeyboardEvent, type ReactElement } from "react";
import { convertImage, hasSendableContent, validateImageBatch, type ImageInput } from "../domain/images";
import { ImagePreview } from "./ImagePreview";
import { StatusMessage } from "./StatusMessage";

export interface ComposerProps {
  enabled?: boolean;
  focusKey?: string;
  value?: string;
  draft?: string;
  onChange?: (value: string) => void;
  onDraftChange?: (value: string) => void;
  onSelectionChange?: (selection: { start: number; end: number }) => void;
  images?: ImageInput[];
  onImagesChange?: (images: ImageInput[]) => void;
  onSend: (body: string, images: ImageInput[]) => void | Promise<void>;
  convert?: (file: Blob & { name?: string }) => Promise<ImageInput>;
}
function normalizeNewlines(value: string): string { return value.replace(/\\r\\n|\\n|\\r/g, "\n"); }
export function Composer({ enabled = true, focusKey, value, draft, onChange, onDraftChange, onSelectionChange, images: controlledImages, onImagesChange, onSend, convert = convertImage }: ComposerProps): ReactElement {
  const [localBody, setLocalBody] = useState(value ?? draft ?? ""); const [localImages, setLocalImages] = useState<ImageInput[]>(controlledImages ?? []); const [dragging, setDragging] = useState(false); const [expanded, setExpanded] = useState(false); const [error, setError] = useState<string>(); const inputRef = useRef<HTMLInputElement>(null); const lastSelection = useRef<{ start: number; end: number } | undefined>(undefined);
  const textareaRef = useRef<HTMLTextAreaElement>(null); const body = value ?? draft ?? localBody; const images = controlledImages ?? localImages;
  useEffect(() => { if (focusKey !== undefined) textareaRef.current?.focus(); }, [focusKey]);
  const setBody = (next: string): void => { setLocalBody(next); onChange?.(next); onDraftChange?.(next); };
  const reportSelection = (selection: { start: number; end: number }): void => { if (lastSelection.current?.start === selection.start && lastSelection.current.end === selection.end) return; lastSelection.current = selection; onSelectionChange?.(selection); };
  const reportTextareaSelection = (target: HTMLTextAreaElement): void => { reportSelection({ start: target.selectionStart, end: target.selectionEnd }); };
  const setImages = (next: ImageInput[]): void => { setLocalImages(next); onImagesChange?.(next); };
  const addFiles = async (files: FileList | File[]): Promise<void> => { const candidates = Array.from(files).filter((file) => file.type.startsWith("image/")); if (!candidates.length) return; if (images.length + candidates.length > 3) { setError("Attach up to three images."); return; } try { const added: ImageInput[] = []; for (const file of candidates) added.push(await convert(file)); const problem = validateImageBatch([...images, ...added]); if (problem) { setError(problem); return; } setError(undefined); setImages([...images, ...added]); } catch (cause) { setError(cause instanceof Error ? cause.message : "Image could not be attached."); } };
  const submit = (event?: FormEvent): void => { event?.preventDefault(); const normalized = normalizeNewlines(body); if (!enabled || !hasSendableContent(normalized, images)) { setError("Enter a message or attach an image."); return; } const problem = validateImageBatch(images); if (problem) { setError(problem); return; } setError(undefined); try { void Promise.resolve(onSend(normalized, images)).catch(() => undefined); } catch (cause) { setError(cause instanceof Error ? cause.message : "Message could not be sent."); return; } setBody(""); setImages([]); };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>): void => {
    if (event.key === "Enter") {
      // In expanded mode: Ctrl+Enter or Cmd+Enter sends; regular Enter inserts newline.
      // In normal/collapsed mode: Enter sends; Shift+Enter inserts newline.
      if (expanded) {
        if (event.ctrlKey || event.metaKey) {
          event.preventDefault();
          submit();
        }
        return;
      }
      if (!event.shiftKey) {
        event.preventDefault();
        submit();
        return;
      }
      const textarea = event.currentTarget;
      const start = textarea.selectionStart;
      const end = textarea.selectionEnd;
      const next = `${body.slice(0, start)}\n${body.slice(end)}`;
      setBody(next);
      reportSelection({ start: start + 1, end: start + 1 });
      requestAnimationFrame(() => {
        if (!textarea.isConnected) return;
        textarea.selectionStart = start + 1;
        textarea.selectionEnd = start + 1;
      });
    } else if (event.key === "Escape" && expanded) {
      event.preventDefault();
      setExpanded(false);
    }
  };
  const onFileChange = (event: ChangeEvent<HTMLInputElement>): void => { if (event.currentTarget.files) void addFiles(event.currentTarget.files); event.currentTarget.value = ""; };
  const onPaste = (event: ClipboardEvent<HTMLTextAreaElement>): void => { const files = event.clipboardData ? Array.from(event.clipboardData.files) : []; if (files.length) { event.preventDefault(); void addFiles(files); } };
  const onDrop = (event: DragEvent<HTMLFormElement>): void => { event.preventDefault(); setDragging(false); void addFiles(event.dataTransfer.files); };
  return (
    <form className={`composer${dragging ? " dragging" : ""}${expanded ? " expanded" : ""}`} onSubmit={submit} onDragOver={(event) => { event.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)} onDrop={onDrop}>
      <ImagePreview images={images} onRemove={(index) => setImages(images.filter((_, candidate) => candidate !== index))} />
      {error ? <StatusMessage tone="error">{error}</StatusMessage> : null}
      <div className={`composer-row${expanded ? " composer-row-expanded" : ""}`}>
        <button className="secondary attach-image" type="button" disabled={!enabled} onClick={() => inputRef.current?.click()} aria-label="Attach images" title="Attach images">
          <i className="fa-solid fa-paperclip" aria-hidden="true" />
        </button>
        <input ref={inputRef} type="file" accept="image/*" multiple hidden onChange={onFileChange} />
        <textarea
          ref={textareaRef}
          data-testid="clipboard-paste-target"
          aria-label="Message"
          rows={expanded ? 10 : 2}
          value={body}
          disabled={!enabled}
          onChange={(event) => { setBody(event.currentTarget.value); reportTextareaSelection(event.currentTarget); }}
          onSelect={(event) => reportTextareaSelection(event.currentTarget)}
          onClick={(event) => reportTextareaSelection(event.currentTarget)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          placeholder={expanded ? "Draft a message, prompt, plan, or snippet (Ctrl+Enter to send, Esc to collapse)..." : "Message an agent or drop images here"}
        />
        <button
          type="button"
          className="secondary composer-expand-btn"
          disabled={!enabled}
          onClick={() => {
            setExpanded((prev) => !prev);
            requestAnimationFrame(() => textareaRef.current?.focus());
          }}
          aria-label={expanded ? "Collapse composer" : "Expand composer"}
          title={expanded ? "Collapse (Esc)" : "Expand for long-form input"}
          style={{ width: "42px", minWidth: "42px", padding: 0 }}
        >
          <i className={`fa-solid ${expanded ? "fa-compress" : "fa-expand"}`} aria-hidden="true" />
        </button>
        <button
          type="submit"
          className="composer-send-btn"
          disabled={!enabled || (!body.trim() && images.length === 0)}
        >
          <i className="fa-solid fa-paper-plane" style={{ marginRight: 6 }} aria-hidden="true" />
          Send
        </button>
      </div>
    </form>
  );
}
export { normalizeNewlines };
