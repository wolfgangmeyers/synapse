import { useEffect, useMemo, useRef, useState } from "react";
import { parsePromptTemplateImport, parsePromptTemplateTokens, promptTemplateExportPayload, renderPromptTemplate, slugifyPromptTemplate, type PromptTemplate, type PromptTemplateExport } from "../domain/prompt-templates";

import { DialogShell } from "./DialogShell";
import { MAX_PROMPT_TEMPLATE_IMPORTS, planPromptTemplateImport, type PromptTemplateImportProgress, type PromptTemplateImportResult } from "../domain/prompt-template-import";
import type { RouterHttpClient } from "../transport/http-client";

export interface PromptTemplateDialogProps {
  open: boolean;
  templates: PromptTemplate[];
  loaded?: boolean;
  templateToUse?: PromptTemplate;
  selection?: { start: number; end: number };
  client?: RouterHttpClient;
  onReloadTemplates?: () => void | Promise<void>;
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

export function PromptTemplateDialog({ open, templates, loaded = true, templateToUse, selection, client, onReloadTemplates, onClose, onClosed, onSave, onDelete, onImport, onExport, importProgress, onInsert }: PromptTemplateDialogProps) {
  const [mode, setMode] = useState<Mode>(templateToUse ? "use" : "list");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<PromptTemplate>({ name: "", body: "" });
  const [values, setValues] = useState<Record<string, string>>({});
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const [status, setStatus] = useState<{ message: string; error?: boolean }>({ message: "" });
  const [localImportProgress, setLocalImportProgress] = useState<PromptTemplateImportProgress>();
  const fileRef = useRef<HTMLInputElement>(null);

  // GitHub Sync state
  const [gitRepoUrl, setGitRepoUrl] = useState("");
  const [savedRepoUrl, setSavedRepoUrl] = useState("");
  const [syncLoading, setSyncLoading] = useState(false);
  const [savingTarget, setSavingTarget] = useState(false);
  const [confirmPull, setConfirmPull] = useState(false);
  const [syncWarning, setSyncWarning] = useState<string | null>(null);

  // Load git sync target on open
  useEffect(() => {
    if (!open || !client) return;
    let active = true;
    client.getGitSyncTarget("prompt_templates").then((res) => {
      if (!active) return;
      if (res && res.repo_url) {
        setGitRepoUrl(res.repo_url);
        setSavedRepoUrl(res.repo_url);
      } else {
        setGitRepoUrl("");
        setSavedRepoUrl("");
      }
    }).catch(() => {});
    return () => { active = false; };
  }, [open, client]);

  useEffect(() => {
    if (open) {
      setMode(templateToUse ? "use" : "list");
      setEditingId(null);
      setValues({});
      setStatus({ message: "" });
      setLocalImportProgress(undefined);
      setQuery("");
      setConfirmPull(false);
      setSyncWarning(null);
    }
  }, [open, templateToUse]);

  const handleSaveRepoUrl = async (newUrl?: string): Promise<void> => {
    const urlToSave = (newUrl !== undefined ? newUrl : gitRepoUrl).trim();
    if (!client || savingTarget) return;
    if (urlToSave === savedRepoUrl) return;
    if (!urlToSave) {
      void handleClearRepo();
      return;
    }
    setSavingTarget(true);
    try {
      const res = await client.setGitSyncTarget("prompt_templates", urlToSave);
      setGitRepoUrl(res.repo_url);
      setSavedRepoUrl(res.repo_url);
      setStatus({ message: "Repository URL saved." });
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Failed to save repository URL.", error: true });
    } finally {
      setSavingTarget(false);
    }
  };

  const handleClearRepo = async (): Promise<void> => {
    if (!client || savingTarget || syncLoading) return;
    if (!savedRepoUrl && !gitRepoUrl) return;
    setSavingTarget(true);
    try {
      if (savedRepoUrl) {
        await client.clearGitSyncTarget("prompt_templates");
      }
      setGitRepoUrl("");
      setSavedRepoUrl("");
      setStatus({ message: "Repository URL cleared." });
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Failed to clear repository URL.", error: true });
    } finally {
      setSavingTarget(false);
    }
  };

  const handlePullConfirm = async (): Promise<void> => {
    if (!client || syncLoading) return;
    setConfirmPull(false);
    setSyncLoading(true);
    setSyncWarning(null);
    setStatus({ message: "" });
    try {
      const res = await client.pullGitSync("prompt_templates");
      if (res.exists === false) {
        setSyncWarning("No prompt-templates.json found in this repository. Nothing was changed.");
      } else if (res.success === false) {
        setStatus({ message: res.error || "Git pull failed.", error: true });
      } else {
        const count = typeof res.count === "number" ? res.count : (res.templates?.length ?? 0);
        setStatus({ message: `Successfully pulled ${count} template${count === 1 ? "" : "s"} from GitHub.` });
        if (onReloadTemplates) {
          await onReloadTemplates();
        }
      }
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Git pull failed.", error: true });
    } finally {
      setSyncLoading(false);
    }
  };

  const handlePush = async (): Promise<void> => {
    if (!client || syncLoading) return;
    setSyncLoading(true);
    setSyncWarning(null);
    setStatus({ message: "" });
    try {
      const res = await client.pushGitSync("prompt_templates");
      if (res.success === false) {
        setStatus({ message: res.error || "Git push failed.", error: true });
      } else {
        const count = typeof res.count === "number" ? res.count : templates.length;
        setStatus({ message: `Successfully pushed ${count} template${count === 1 ? "" : "s"} to GitHub.` });
      }
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Git push failed.", error: true });
    } finally {
      setSyncLoading(false);
    }
  };

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
    try {
      if (editingId) {
        await onSave({ ...draft, id: editingId, name });
      } else {
        const id = draft.id?.trim() || slugifyPromptTemplate(name);
        await onSave({ ...draft, id, name });
      }
      setMode("list");
      setEditingId(null);
      setStatus({ message: "Template saved." });
    }
    catch (cause) { setStatus({ message: cause instanceof Error ? cause.message : "Template could not be saved.", error: true }); }
  };
  const remove = async (id: string): Promise<void> => {
    try { await onDelete(id); setDeleteId(null); setStatus({ message: "Template deleted." }); }
    catch (cause) { setStatus({ message: cause instanceof Error ? cause.message : "Template could not be deleted.", error: true }); }
  };
  const filteredTemplates = useMemo(() => {
    const trimmed = query.trim().toLowerCase();
    if (!trimmed) return templates;
    return templates.filter((t) => t.name.toLowerCase().includes(trimmed) || (t.id && t.id.toLowerCase().includes(trimmed)) || t.body.toLowerCase().includes(trimmed));
  }, [templates, query]);
  const title = mode === "editor" ? (editingId ? "Edit prompt template" : "New prompt template") : mode === "use" ? "Use prompt template" : "Prompt templates";
  const effectiveSlug = draft.id !== undefined && draft.id !== "" ? draft.id : slugifyPromptTemplate(draft.name);
  return <DialogShell open={open} title={title} titleId={mode === "use" ? "prompt-use-dialog-title" : "prompt-templates-dialog-title"} description={mode === "use" ? (tokens.length ? "Fill in the values before pasting this prompt." : "Paste this prompt into the composer.") : "Save prompts you use often."} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close prompt template manager" className="message-dialog prompt-dialog">
    {!loaded && mode === "list" ? <p className="prompt-dialog-empty">Loading prompt templates…</p> : mode === "editor" ? <form className="prompt-template-editor-form" onSubmit={(event) => { event.preventDefault(); save(); }}><label>Template name<input maxLength={128} value={draft.name} placeholder="For example, Review pull request" onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value }))} /></label><label>Template ID (slug)<input maxLength={128} value={draft.id ?? ""} disabled={Boolean(editingId)} placeholder={slugifyPromptTemplate(draft.name) || "code-review"} onChange={(event) => setDraft((current) => ({ ...current, id: event.target.value }))} /><small style={{ color: "var(--muted, #888)", fontSize: "0.8rem", fontWeight: "normal" }}>Inclusion hint: <code style={{ userSelect: "all" }}>{`{{ .${effectiveSlug || "template-id"} }}`}</code></small></label><label>Prompt body<textarea maxLength={64 * 1024} rows={10} value={draft.body} placeholder="Write the prompt text or Markdown" onChange={(event) => setDraft((current) => ({ ...current, body: event.target.value }))} /></label>{status.message ? <p className={`prompt-template-dialog-status${status.error ? " error" : ""}`} role={status.error ? "alert" : "status"}>{status.message}</p> : null}<Actions cancel={() => { setMode("list"); setEditingId(null); setStatus({ message: "" }); }} primary={editingId ? "Save changes" : "Create template"} /></form> : mode === "use" && templateToUse ? <form className="prompt-use-form" onSubmit={(event) => { event.preventDefault(); onInsert?.(preview, selection); onClose(); }}>{tokens.map((token) => <label key={token}>{token}<input value={values[token] ?? ""} onChange={(event) => setValues((current) => ({ ...current, [token]: event.target.value }))} /></label>)}<pre className="prompt-template-preview">{preview}</pre><Actions cancel={onClose} primary="Paste" /></form> : <div className="prompt-dialog-body">
      {client ? (
        <section className="prompt-github-sync" aria-label="GitHub Sync">
          <div className="prompt-github-sync-header">
            <i className="fa-brands fa-github" aria-hidden="true" />
            <span>GitHub Sync</span>
          </div>
          <div className="prompt-github-sync-row">
            <div className="prompt-github-input-wrap">
              <input
                type="text"
                className="prompt-github-url-input"
                placeholder="git@github.com:owner/repo.git or https://github.com/owner/repo.git"
                value={gitRepoUrl}
                disabled={syncLoading || savingTarget}
                onChange={(e) => setGitRepoUrl(e.target.value)}
                onBlur={() => { void handleSaveRepoUrl(); }}
                onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void handleSaveRepoUrl(); } }}
              />
              {gitRepoUrl || savedRepoUrl ? (
                <button
                  type="button"
                  className="prompt-github-input-clear"
                  onClick={() => void handleClearRepo()}
                  title="Clear repository URL"
                  aria-label="Clear repository URL"
                  disabled={syncLoading || savingTarget}
                >
                  <i className="fa-solid fa-xmark" aria-hidden="true" />
                </button>
              ) : null}
            </div>
            <button
              type="button"
              className="prompt-github-save-btn"
              disabled={!gitRepoUrl.trim() || gitRepoUrl.trim() === savedRepoUrl || syncLoading || savingTarget}
              onClick={() => void handleSaveRepoUrl()}
            >
              {savingTarget ? "Saving…" : "Save"}
            </button>
            <div className="prompt-github-sync-actions">
              <button
                type="button"
                className="icon-btn-round"
                title="Pull from GitHub"
                aria-label="Pull from GitHub"
                disabled={!savedRepoUrl || syncLoading || savingTarget}
                onClick={() => setConfirmPull(true)}
              >
                <i className={`fa-solid ${syncLoading ? "fa-spinner fa-spin" : "fa-cloud-arrow-down"}`} aria-hidden="true" />
              </button>
              <button
                type="button"
                className="icon-btn-round"
                title="Push to GitHub"
                aria-label="Push to GitHub"
                disabled={!savedRepoUrl || syncLoading || savingTarget}
                onClick={() => void handlePush()}
              >
                <i className={`fa-solid ${syncLoading ? "fa-spinner fa-spin" : "fa-cloud-arrow-up"}`} aria-hidden="true" />
              </button>
            </div>
          </div>
          {confirmPull ? (
            <div className="prompt-github-sync-confirm" role="alert">
              <p>This will replace all local prompt templates with the version from GitHub. Continue?</p>
              <div className="prompt-github-sync-confirm-actions">
                <button
                  type="button"
                  className="secondary"
                  disabled={syncLoading}
                  onClick={() => setConfirmPull(false)}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className="danger-button"
                  disabled={syncLoading}
                  onClick={() => void handlePullConfirm()}
                >
                  {syncLoading ? "Pulling…" : "Confirm Pull"}
                </button>
              </div>
            </div>
          ) : null}
          {syncWarning ? (
            <div className="prompt-github-sync-confirm" role="alert">
              <p>{syncWarning}</p>
              <div className="prompt-github-sync-confirm-actions">
                <button
                  type="button"
                  className="secondary"
                  onClick={() => setSyncWarning(null)}
                >
                  Dismiss
                </button>
              </div>
            </div>
          ) : null}
        </section>
      ) : null}
      <div className="prompt-dialog-toolbar"><input className="model-search prompt-template-dialog-search" type="search" aria-label="Search prompt templates" placeholder="Search templates…" value={query} onChange={(e) => setQuery(e.target.value)} /><button type="button" className="action-button" title="Import templates" aria-label="Import templates" disabled={!loaded || effectiveImportProgress !== undefined} onClick={() => fileRef.current?.click()}><i className="fa-solid fa-file-import" /></button><button type="button" data-testid="prompt-export" className="action-button" title="Export templates" aria-label="Export templates" disabled={!loaded || effectiveImportProgress !== undefined} onClick={exportTemplates}><i className="fa-solid fa-file-export" /></button><button type="button" className="action-button" title="New template" aria-label="New template" disabled={effectiveImportProgress !== undefined} onClick={() => { setDraft({ name: "", body: "" }); setEditingId(null); setMode("editor"); }}><i className="fa-solid fa-plus" /></button><input ref={fileRef} hidden type="file" accept=".json,application/json" onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) void readImport(file); }} /></div>{effectiveImportProgress ? <section className="prompt-template-import-confirmation" aria-live="polite"><h3>{effectiveImportProgress.interrupted ? "Import interrupted" : "Importing templates"}</h3><p>{effectiveImportProgress.interrupted ? `Import stopped after ${effectiveImportProgress.completed} of ${effectiveImportProgress.total} templates.` : `Imported ${effectiveImportProgress.completed} of ${effectiveImportProgress.total} templates.`}</p></section> : null}{filteredTemplates.length === 0 ? <p className="prompt-dialog-empty">{templates.length === 0 ? "No saved prompt templates." : "No matching prompt templates"}</p> : filteredTemplates.map((template) => <article className="prompt-template-item" key={template.id ?? template.name}><div style={{ display: "flex", alignItems: "baseline", gap: "8px", flexWrap: "wrap" }}><strong>{template.name}</strong><code style={{ fontSize: "0.78rem", color: "var(--muted, #888)", userSelect: "all" }}>{`{{ .${template.id ?? slugifyPromptTemplate(template.name)} }}`}</code></div><pre>{template.body}</pre>{deleteId === (template.id ?? template.name) ? <><p className="prompt-delete-confirmation">Delete this template?</p><div className="prompt-template-actions"><button type="button" className="secondary" onClick={() => setDeleteId(null)}>Cancel</button><button type="button" className="danger-button" onClick={() => { if (template.id) void remove(template.id); }}>Delete</button></div></> : <div className="prompt-template-actions"><button type="button" className="secondary" onClick={() => { const id = template.id ?? slugifyPromptTemplate(template.name); setDraft({ ...template, id }); setEditingId(id); setMode("editor"); }}>Edit</button><button type="button" className="secondary" onClick={() => setDeleteId(template.id ?? template.name)}>Delete</button></div>}</article>)}{status.message ? <p className={`prompt-template-dialog-status${status.error ? " error" : ""}`} role={status.error ? "alert" : "status"}>{status.message}</p> : null}</div>}

  </DialogShell>;
}

function Actions({ cancel, primary }: { cancel: () => void; primary: string }) { return <div className="prompt-template-actions"><button type="button" className="secondary" onClick={cancel}>Cancel</button><button type="submit">{primary}</button></div>; }
