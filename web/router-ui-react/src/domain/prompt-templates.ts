import { parseTemplateTokens, renderTemplate } from "./template-engine";

export const PROMPT_TEMPLATE_EXPORT_FORMAT = "mecha-prompt-templates";
export const PROMPT_TEMPLATE_EXPORT_VERSION = 1;
export const MAX_PROMPT_TEMPLATES = 100;
export const MAX_PROMPT_TEMPLATE_NAME_BYTES = 128;
export const MAX_PROMPT_TEMPLATE_BODY_BYTES = 64 * 1024;
export const MAX_PROMPT_TEMPLATE_ID_BYTES = 1024;
const TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const textEncoder = new TextEncoder();
export interface PromptTemplate { name: string; body: string; id?: string; updated_at?: string }
export interface PromptTemplateExport { format: typeof PROMPT_TEMPLATE_EXPORT_FORMAT; version: 1; templates: PromptTemplate[] }
function objectValue(value: unknown): value is Record<string, unknown> { return value !== null && typeof value === "object" && !Array.isArray(value); }
function canonicalTimestamp(value: unknown): value is string { if (typeof value !== "string" || !TIMESTAMP.test(value)) return false; try { return new Date(value).toISOString() === value; } catch { return false; } }
export function isPortablePromptTemplateId(value: unknown): value is string { if (typeof value !== "string" || value.length === 0 || value.includes("\u0000") || textEncoder.encode(value).length > MAX_PROMPT_TEMPLATE_ID_BYTES) return false; for (let index = 0; index < value.length; index += 1) { const code = value.charCodeAt(index); if (code >= 0xD800 && code <= 0xDBFF) { const next = value.charCodeAt(index + 1); if (!(next >= 0xDC00 && next <= 0xDFFF)) return false; index += 1; } else if (code >= 0xDC00 && code <= 0xDFFF) return false; } return true; }
export function promptTemplateExportPayload(templates: unknown): PromptTemplateExport { const values = Array.isArray(templates) ? templates : []; return { format: PROMPT_TEMPLATE_EXPORT_FORMAT, version: 1, templates: values.map((value) => { const input = objectValue(value) ? value : {}; const result: PromptTemplate = { name: String(input.name ?? ""), body: String(input.body ?? "") }; if (typeof input.id === "string") result.id = input.id; if (canonicalTimestamp(input.updated_at)) result.updated_at = input.updated_at; return result; }) }; }
export function promptTemplateUpdatePayload(template: PromptTemplate): { name: string; body: string; updated_at?: string } { return { name: template.name, body: template.body, ...(canonicalTimestamp(template.updated_at) ? { updated_at: template.updated_at } : {}) }; }
export function parsePromptTemplateImport(value: unknown): PromptTemplate[] { let parsed: unknown; try { parsed = JSON.parse(String(value)); } catch { throw new Error("Choose a valid prompt template JSON file."); } if (!objectValue(parsed) || parsed.format !== PROMPT_TEMPLATE_EXPORT_FORMAT || parsed.version !== 1 || !Array.isArray(parsed.templates) || parsed.templates.length > MAX_PROMPT_TEMPLATES) throw new Error("This is not a valid prompt template export file."); const names = new Set<string>(); const ids = new Set<string>(); const result: PromptTemplate[] = []; parsed.templates.forEach((value, index) => { if (!objectValue(value) || Object.keys(value).some((key) => !["name", "body", "id", "updated_at"].includes(key))) throw new Error(`Template ${index + 1} has invalid metadata.`); if (typeof value.name !== "string" || typeof value.body !== "string" || value.name.includes("\u0000") || value.body.includes("\u0000") || !value.name.trim() || !value.body.trim() || textEncoder.encode(value.name).length > MAX_PROMPT_TEMPLATE_NAME_BYTES || textEncoder.encode(value.body).length > MAX_PROMPT_TEMPLATE_BODY_BYTES) throw new Error(`Template ${index + 1} must have a valid name and body.`); if (Object.hasOwn(value, "id")) { if (!isPortablePromptTemplateId(value.id)) throw new Error(`Template ${index + 1} has invalid metadata.`); if (ids.has(value.id)) throw new Error(`The import contains duplicate template id \"${value.id}\".`); ids.add(value.id); } if (Object.hasOwn(value, "updated_at") && !canonicalTimestamp(value.updated_at)) throw new Error(`Template ${index + 1} has invalid metadata.`); if (names.has(value.name)) throw new Error(`The import contains duplicate template name \"${value.name}\".`); names.add(value.name); result.push({ ...(typeof value.id === "string" ? { id: value.id } : {}), name: value.name, body: value.body, ...(typeof value.updated_at === "string" ? { updated_at: value.updated_at } : {}) }); }); return result; }
export const parsePromptTemplateTokens = parseTemplateTokens;
export const renderPromptTemplate = renderTemplate;
export interface ImportProgress { completed: number; total: number; interrupted: boolean; message: string }
export function promptTemplateImportProgress(completed: number, total: number, interrupted = false): ImportProgress { return { completed, total, interrupted, message: interrupted ? `Import stopped after ${completed} of ${total} templates.` : `Imported ${completed} of ${total} templates.` }; }

export const PROMPT_TEMPLATE_USAGE_STORAGE_KEY = "mecha.prompt_template_usage";

export function promptTemplateKey(template: PromptTemplate): string {
  return template.id ?? template.name;
}

export function loadPromptTemplateUsage(storage?: { getItem(key: string): string | null }): Record<string, number> {
  try {
    const raw = storage?.getItem(PROMPT_TEMPLATE_USAGE_STORAGE_KEY) ?? (typeof localStorage !== "undefined" ? localStorage.getItem(PROMPT_TEMPLATE_USAGE_STORAGE_KEY) : null);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return Object.fromEntries(Object.entries(parsed).filter((entry): entry is [string, number] => typeof entry[1] === "number" && Number.isFinite(entry[1])));
    }
  } catch {
    // Ignore storage parse errors
  }
  return {};
}

export function recordPromptTemplateUsage(
  template: PromptTemplate,
  usage: Record<string, number>,
  nowOrStorage?: number | { setItem(key: string, value: string): void },
  storage?: { setItem(key: string, value: string): void },
): Record<string, number> {
  const now = typeof nowOrStorage === "number" ? nowOrStorage : Date.now();
  const effectiveStorage = typeof nowOrStorage === "object" && nowOrStorage !== null ? nowOrStorage : storage;
  const key = promptTemplateKey(template);
  const next = { ...usage, [key]: now };
  if (template.id && template.name && key !== template.name) {
    next[template.name] = now;
  }
  try {
    const serialized = JSON.stringify(next);
    if (effectiveStorage) effectiveStorage.setItem(PROMPT_TEMPLATE_USAGE_STORAGE_KEY, serialized);
    else if (typeof localStorage !== "undefined") localStorage.setItem(PROMPT_TEMPLATE_USAGE_STORAGE_KEY, serialized);
  } catch {
    // Ignore storage quota/security errors
  }
  return next;
}

export function sortPromptTemplatesByMRU(templates: PromptTemplate[], usage?: Record<string, number>): PromptTemplate[] {
  const safeUsage = usage ?? {};
  return [...templates].sort((a, b) => {
    const usageA = Math.max(safeUsage[a.id ?? ""] ?? 0, safeUsage[a.name] ?? 0);
    const usageB = Math.max(safeUsage[b.id ?? ""] ?? 0, safeUsage[b.name] ?? 0);
    if (usageA !== usageB) return usageB - usageA;
    return a.name.localeCompare(b.name);
  });
}

