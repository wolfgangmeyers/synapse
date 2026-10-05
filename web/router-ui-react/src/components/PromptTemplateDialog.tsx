import { useEffect, useMemo, useRef, useState } from "react";
import { parsePromptTemplateImport, parsePromptTemplateTokens, promptTemplateExportPayload, renderPromptTemplate, type PromptTemplate, type PromptTemplateExport } from "../domain/prompt-templates";
import { DialogShell } from "./DialogShell";
import { MAX_PROMPT_TEMPLATE_IMPORTS, planPromptTemplateImport, type PromptTemplateImportProgress, type PromptTemplateImportResult } from "../domain/prompt-template-import";

export interface PromptTemplateDialogProps {
  open: boolean;
  templates: PromptTemplate[];
  loaded?: boolean;
  templateToUse?: PromptTemplate;
  selection?: { start: number; end: number };
  onClose: () => void;
  onClosed?: () => void;
  onSave: (template: PromptTemplate) => void | Promise<void>;
  onDelete: (id: string) => void | Promise<void>;
  onImport: (templates: PromptTemplate[], overwrite: boolean) => void | Promise<PromptTemplateImportResult>;
  onExport?: (payload: PromptTemplateExport) => void;
  importProgress?: PromptTemplateImportProgress;
  onInsert?: (value: string, selection?: { start: number; end: number }) => void;
}

type Mode = "list" | "editor" | "use";

export function PromptTemplateDialog({ open, templates, loaded = true, templateToUse, selection, onClose, onClosed, onSave, onDelete, onImport, onExport, importProgress, onInsert }: PromptTemplateDialogProps) {
  const [mode, setMode] = useState<Mode>(templateToUse ? "use" : "list");
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<PromptTemplate>({ name: "", body: "" });
  const [values, setValues] = useState<Record<string, string>>({});
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const [status, setStatus] = useState<{ message: string; error?: boolean }>({ message: "" });
  const [localImportProgress, setLocalImportProgress] = useState<PromptTemplateImportProgress>();
  const fileRef = useRef<HTMLInputElement>(null);
  useEffect(() => { if (open) { setMode(templateToUse ? "use" : "list"); setValues({}); setStatus({ message: "" }); setLocalImportProgress(undefined); setQuery(""); } }, [open, templateToUse]);
  const effectiveImportProgress = importProgress ?? localImportProgress;
  const tokens = useMemo(() => templateToUse ? parsePromptTemplateTokens(templateToUse.body) : [], [templateToUse]);
  const preview = templateToUse ? renderPromptTemplate(templateToUse.body, values) : "";
  const exportTemplates = () => {
    const payload = promptTemplateExportPayload(templates);
    try {
      const blob = new Blob([`${JSON.stringify(payload, null, 2)}\n`], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = "prompt-templates.json";
      // Keep the anchor in the document while the browser handles the click.
      // Revoking on the next turn can race Chrome's Blob download startup.
      document.body.append(link);
      try { link.click(); } finally {
        link.remove();
        const revokeObjectURL = URL.revokeObjectURL;
        if (typeof revokeObjectURL === "function") setTimeout(() => revokeObjectURL(url), 100);
      }
      onExport?.(payload);
      setStatus({ message: "Templates exported." });
    } catch { setStatus({ message: "Unable to export prompt templates.", error: true }); }
  };
  const readFileText = (file: File): Promise<string> => {
    if (typeof file.text === "function") return file.text();
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => { if (typeof reader.result === "string") resolve(reader.result); else reject(new Error("Unable to read the prompt template file.")); };
      reader.onerror = () => reject(new Error("Unable to read the prompt template file."));
      reader.readAsText(file);
    });
  };
  const readImport = async (file: File) => {
    try {
      const next = parsePromptTemplateImport(await readFileText(file));
      if (next.length === 0) { setStatus({ message: "The file contains no templates." }); return; }
      setLocalImportProgress({ completed: 0, total: next.length, interrupted: false });
      setStatus({ message: "" });
      const result = await onImport(next, true);
      setLocalImportProgress(undefined);
      const importedCount = result?.imported ?? next.length;
      const skippedCount = result?.skipped ?? 0;
      setStatus({ message: `Imported ${importedCount}, skipped ${skippedCount}.` });
    } catch (value) {
      setLocalImportProgress((current) => ({ completed: current?.completed ?? 0, total: 0, interrupted: true }));
      setStatus({ message: value instanceof Error ? value.message : "Unable to read the prompt template file.", error: true });
    }
  };
  const save = async (): Promise<void> => {
    const name = draft.name.trim();
    if (!name) { setStatus({ message: "Enter a template name.", error: true }); return; }
    if (!draft.body.trim()) { setStatus({ message: "Enter a prompt body.", error: true }); return; }
    if (new TextEncoder().encode(name).length > 128) { setStatus({ message: "Template name is too long.", error: true }); return; }
    if (new TextEncoder().encode(draft.body).length > 64 * 1024) { setStatus({ message: "Prompt body is too long.", error: true }); return; }
    try { await onSave({ ...draft, name }); setMode("list"); setStatus({ message: "Template saved." }); }
    catch (cause) { setStatus({ message: cause instanceof Error ? cause.message : "Template could not be saved.", error: true }); }
  };
  const remove = async (id: string): Promise<void> => {
    try { await onDelete(id); setDeleteId(null); setStatus({ message: "Template deleted." }); }
    catch (cause) { setStatus({ message: cause instanceof Error ? cause.message : "Template could not be deleted.", error: true }); }
  };
  const filteredTemplates = useMemo(() => {
    const trimmed = query.trim().toLowerCase();
    if (!trimmed) return templates;
    return templates.filter((t) => t.name.toLowerCase().includes(trimmed) || t.body.toLowerCase().includes(trimmed));
  }, [templates, query]);
  const title = mode === "editor" ? (draft.id ? "Edit prompt template" : "New prompt template") : mode === "use" ? "Use prompt template" : "Prompt templates";
  return <DialogShell open={open} title={title} titleId={mode === "use" ? "prompt-use-dialog-title" : "prompt-templates-dialog-title"} description={mode === "use" ? (tokens.length ? "Fill in the values before pasting this prompt." : "Paste this prompt into the composer.") : "Save prompts you use often."} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close prompt template manager" className="message-dialog prompt-dialog">
    {!loaded && mode === "list" ? <p className="prompt-dialog-empty">Loading prompt templates…</p> : mode === "editor" ? <form className="prompt-template-editor-form" onSubmit={(event) => { event.preventDefault(); save(); }}><label>Template name<input maxLength={128} value={draft.name} placeholder="For example, Review pull request" onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))} /></label><label>Prompt body<textarea maxLength={64 * 1024} rows={10} value={draft.body} placeholder="Write the prompt text or Markdown" onChange={(event) => setDraft((current) => ({ ...current, body: event.target.value }))} /></label>{status.message ? <p className={`prompt-template-dialog-status${status.error ? " error" : ""}`} role={status.error ? "alert" : "status"}>{status.message}</p> : null}<Actions cancel={() => { setMode("list"); setStatus({ message: "" }); }} primary={draft.id ? "Save changes" : "Create template"} /></form> : mode === "use" && templateToUse ? <form className="prompt-use-form" onSubmit={(event) => { event.preventDefault(); onInsert?.(preview, selection); onClose(); }}>{tokens.map((token) => <label key={token}>{token}<input value={values[token] ?? ""} onChange={(event) => setValues((current) => ({ ...current, [token]: event.target.value }))} /></label>)}<pre className="prompt-template-preview">{preview}</pre><Actions cancel={onClose} primary="Paste" /></form> : <div className="prompt-dialog-body"><div className="prompt-dialog-toolbar"><input className="model-search prompt-template-dialog-search" type="search" aria-label="Search prompt templates" placeholder="Search templates…" value={query} onChange={(e) => setQuery(e.target.value)} /><button type="button" disabled={!loaded || effectiveImportProgress !== undefined} onClick={() => fileRef.current?.click()}>Import</button><button type="button" data-testid="prompt-export" className="secondary" disabled={!loaded || effectiveImportProgress !== undefined} onClick={exportTemplates}>Export</button><button type="button" className="icon-button prompt-dialog-add" aria-label="New template" disabled={effectiveImportProgress !== undefined} onClick={() => { setDraft({ name: "", body: "" }); setMode("editor"); }}>+</button><input ref={fileRef} hidden type="file" accept=".json,application/json" onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) void readImport(file); }} /></div>{effectiveImportProgress ? <section className="prompt-template-import-confirmation" aria-live="polite"><h3>{effectiveImportProgress.interrupted ? "Import interrupted" : "Importing templates"}</h3><p>{effectiveImportProgress.interrupted ? `Import stopped after ${effectiveImportProgress.completed} of ${effectiveImportProgress.total} templates.` : `Imported ${effectiveImportProgress.completed} of ${effectiveImportProgress.total} templates.`}</p></section> : null}{filteredTemplates.length === 0 ? <p className="prompt-dialog-empty">{templates.length === 0 ? "No saved prompt templates." : "No matching prompt templates"}</p> : filteredTemplates.map((template) => <article className="prompt-template-item" key={template.id ?? template.name}><strong>{template.name}</strong><pre>{template.body}</pre>{deleteId === (template.id ?? template.name) ? <><p className="prompt-delete-confirmation">Delete this template?</p><div className="prompt-template-actions"><button type="button" className="secondary" onClick={() => setDeleteId(null)}>Cancel</button><button type="button" className="danger-button" onClick={() => { if (template.id) void remove(template.id); }}>Delete</button></div></> : <div className="prompt-template-actions"><button type="button" className="secondary" onClick={() => { setDraft({ ...template }); setMode("editor"); }}>Edit</button><button type="button" className="secondary" onClick={() => setDeleteId(template.id ?? template.name)}>Delete</button></div>}</article>)}{status.message ? <p className={`prompt-template-dialog-status${status.error ? " error" : ""}`} role={status.error ? "alert" : "status"}>{status.message}</p> : null}</div>}
  </DialogShell>;
}

function Actions({ cancel, primary }: { cancel: () => void; primary: string }) { return <div className="prompt-template-actions"><button type="button" className="secondary" onClick={cancel}>Cancel</button><button type="submit">{primary}</button></div>; }
