import type { PromptTemplate } from "./prompt-templates";

export const PROMPT_TEMPLATE_IMPORT_TIMEOUT_MS = 15_000;
export const MAX_PROMPT_TEMPLATE_IMPORTS = 100;
export interface PromptTemplateImportOperations {
  create(template: PromptTemplate): Promise<void>;
  update(id: string, template: PromptTemplate): Promise<void>;
  refresh(): Promise<void>;
}
export interface PromptTemplateImportProgress { completed: number; total: number; interrupted: boolean; message?: string }
export interface PromptTemplateImportResult { imported: number; skipped: number }
export interface PromptTemplateImportPlanItem { template: PromptTemplate; action: "create" | "update" | "skip"; id?: string; reason?: "name_conflict" | "missing_timestamp" }
export class PromptTemplateImportError extends Error {
  constructor(message: string, readonly completed: number, readonly total: number, readonly imported: number, readonly skipped: number) { super(message); this.name = "PromptTemplateImportError"; }
}
interface PromptTemplateImportErrorMetadata {
  status?: number;
  code?: string;
}
class NormalizedPromptTemplateImportError extends Error {
  readonly status?: number;
  readonly code?: string;
  constructor(message: string, metadata: PromptTemplateImportErrorMetadata) {
    super(message);
    if (metadata.status !== undefined) this.status = metadata.status;
    if (metadata.code !== undefined) this.code = metadata.code;
  }
}
function normalizeImportRejection(cause: unknown): Error {
  if (cause instanceof Error) return cause;
  if (cause !== null && typeof cause === "object") {
    const status = "status" in cause && typeof cause.status === "number" ? cause.status : undefined;
    const code = "code" in cause && typeof cause.code === "string" ? cause.code : undefined;
    const message = "message" in cause && typeof cause.message === "string" ? cause.message : String(cause);
    return new NormalizedPromptTemplateImportError(message, { ...(status === undefined ? {} : { status }), ...(code === undefined ? {} : { code }) });
  }
  return new Error(String(cause));
}
function timeout<T>(promise: Promise<T>, timeoutMs: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("Template import timed out.")), timeoutMs);
    promise.then((value) => { clearTimeout(timer); resolve(value); }, (cause: unknown) => { clearTimeout(timer); reject(normalizeImportRejection(cause)); });
  });
}
function isPromptTemplateNameConflict(cause: unknown): boolean {
  if (cause === null || typeof cause !== "object" || !("status" in cause) || !("code" in cause)) return false;
  return cause.status === 409 && cause.code === "prompt_template_name_conflict";
}
export function planPromptTemplateImport(templates: readonly PromptTemplate[], existing: readonly PromptTemplate[], overwrite = true): PromptTemplateImportPlanItem[] {
  const byId = new Map<string, PromptTemplate & { id: string }>();
  for (const template of existing) {
    if (template.id !== undefined) byId.set(template.id, { ...template, id: template.id });
  }
  const byName = new Map(existing.map((template) => [template.name, template] as const));
  return templates.map((template) => {
    const matchingId = template.id === undefined ? undefined : byId.get(template.id);
    const matchingName = byName.get(template.name);
    if (matchingId !== undefined) {
      if (matchingName !== undefined && matchingName.id !== matchingId.id) return { template, action: "skip", reason: "name_conflict" };
      if (!overwrite || template.updated_at === undefined) return { template, action: "skip", reason: "missing_timestamp" };
      return { template, action: "update", id: matchingId.id };
    }
    if (matchingName !== undefined) return { template, action: "skip", reason: "name_conflict" };
    return { template, action: "create" };
  });
}
export async function runPromptTemplateImport(
  templates: readonly PromptTemplate[],
  overwrite: boolean,
  operations: PromptTemplateImportOperations,
  onProgress?: (progress: PromptTemplateImportProgress) => void,
  options: { timeoutMs?: number; existingTemplates?: readonly PromptTemplate[]; capacity?: number } = {},
): Promise<PromptTemplateImportResult> {
  const total = templates.length;
  const timeoutMs = options.timeoutMs ?? PROMPT_TEMPLATE_IMPORT_TIMEOUT_MS;
  const existing = options.existingTemplates ?? [];
  const plan = planPromptTemplateImport(templates, existing, overwrite);
  const capacity = options.capacity ?? MAX_PROMPT_TEMPLATE_IMPORTS;
  const createCount = plan.filter((item) => item.action === "create").length;
  if (existing.length + createCount > capacity) throw new PromptTemplateImportError("Import would exceed the 100-template limit.", 0, total, 0, plan.filter((item) => item.action === "skip").length);
  let completed = 0;
  let imported = 0;
  let skipped = 0;
  onProgress?.({ completed, total, interrupted: false });
  try {
    for (const item of plan) {
      if (item.action === "skip") {
        skipped += 1;
      } else {
        try {
          const operation = item.action === "update" && item.id !== undefined
            ? operations.update(item.id, { name: item.template.name, body: item.template.body, ...(item.template.updated_at === undefined ? {} : { updated_at: item.template.updated_at }) })
            : operations.create(item.template);
          await timeout(operation, timeoutMs);
          imported += 1;
        } catch (cause) {
          if (!isPromptTemplateNameConflict(cause)) throw cause;
          skipped += 1;
        }
      }
      completed += 1;
      onProgress?.({ completed, total, interrupted: false });
    }
    await operations.refresh();
    return { imported, skipped };
  } catch (cause) {
    try { await operations.refresh(); } catch { /* Preserve the import error and progress. */ }
    const reason = cause instanceof Error ? cause.message : "Template import failed.";
    onProgress?.({ completed, total, interrupted: true });
    throw new PromptTemplateImportError(`Import stopped after ${completed} of ${total} templates. ${reason}`, completed, total, imported, skipped);
  }
}
