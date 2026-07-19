import { randomUUID } from "node:crypto";
import { existsSync, lstatSync, readFileSync, readdirSync, realpathSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve, sep } from "node:path";

export const RECALL_POLICY = Object.freeze({
  inactivity_seconds: 30,
  maximum_window_seconds: 120,
  candidate_limit: 100,
  candidate_bytes: 16384,
  evaluator: "openai-codex/gpt-5.6-luna",
  maximum_messages: 3,
  expansion: Object.freeze({ maximum_ids: 6, maximum_terms: 3, maximum_tags: 8, maximum_matched_records: 12, maximum_bytes: 16384, maximum_related_edges: 24, maximum_graph_depth: 2 }),
  capture: Object.freeze({ evaluator: "openai-codex/gpt-5.6-sol", compaction_deadline_ms: 30000, shutdown_deadline_ms: 10000, evaluator_timeout_ms: 60000, router_timeout_ms: 5000, source_max_bytes: 98304, source_message_max_bytes: 8192, tool_result_max_bytes: 2048, previous_summary_max_bytes: 8192, candidate_limit: 100, candidate_max_bytes: 65536, output_max_bytes: 32768, maximum_records: 6, maximum_title_bytes: 256, maximum_body_bytes_per_record: 16384, maximum_total_body_bytes: 32768, maximum_related_ids_per_record: 24 }),
});
const STORE_ID = /^[a-z0-9][a-z0-9-]{0,63}$/;
const ID = /^[a-z0-9][a-z0-9-]{0,127}$/;
const TAG = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const FLUSH_DELAY_MS = 25;

function validateApplyInput(input) {
  const top = ["store", "request_id", "operations"];
  if (!input || typeof input !== "object" || Array.isArray(input) || Object.keys(input).length !== top.length || Object.keys(input).some((key) => !top.includes(key)) || typeof input.store !== "string" || (input.store !== "default" && !STORE_ID.test(input.store)) || typeof input.request_id !== "string" || input.request_id.length === 0 || input.request_id.length > 128 || !Array.isArray(input.operations) || input.operations.length === 0 || input.operations.length > RECALL_POLICY.capture.maximum_records) throw new MemoryCatalogError("invalid capture apply");
  const seen = new Set(); let totalBodyBytes = 0;
  const validTitle = (value) => typeof value === "string" && value.length > 0 && value === value.trim() && !/[\r\n]/.test(value) && Buffer.byteLength(value) <= RECALL_POLICY.capture.maximum_title_bytes;
  const validBody = (value) => typeof value === "string" && normalizeBody(value).trim().length > 0 && Buffer.byteLength(normalizeBody(value)) <= RECALL_POLICY.capture.maximum_body_bytes_per_record;
  const validRelated = (value) => Array.isArray(value) && value.length <= RECALL_POLICY.capture.maximum_related_ids_per_record && value.every((id) => typeof id === "string" && ID.test(id));
  const validTimestamp = (value) => typeof value === "string" && Number.isFinite(Date.parse(value)) && new Date(value).toISOString() === value;
  for (const operation of input.operations) {
    const update = operation?.mode === "update"; const tagged = Object.hasOwn(operation ?? {}, "tags"); const allowed = update ? tagged ? ["mode", "id", "expected_relevant_at", "expected_title", "expected_body", "expected_tags", "expected_related", "title", "body", "tags", "related"] : ["mode", "id", "expected_relevant_at", "expected_title", "expected_body", "expected_related", "title", "body", "related"] : tagged ? ["mode", "id", "title", "body", "tags", "related"] : ["mode", "id", "title", "body", "related"];
    if (!operation || typeof operation !== "object" || Array.isArray(operation) || !["create", "update"].includes(operation.mode) || Object.keys(operation).length !== allowed.length || Object.keys(operation).some((key) => !allowed.includes(key)) || typeof operation.id !== "string" || !ID.test(operation.id) || seen.has(operation.id) || !validTitle(operation.title) || !validBody(operation.body) || !validRelated(operation.related) || (tagged && (!validTags(operation.tags) || !validProseBody(operation.body))) || (update && (!validTimestamp(operation.expected_relevant_at) || !validTitle(operation.expected_title) || !validBody(operation.expected_body) || !validRelated(operation.expected_related) || (tagged && (!validTags(operation.expected_tags, 0) || (operation.expected_tags.length > 0 && !validProseBody(operation.expected_body))))))) throw new MemoryCatalogError("invalid capture apply");
    seen.add(operation.id); totalBodyBytes += Buffer.byteLength(normalizeBody(operation.body));
    if (totalBodyBytes > RECALL_POLICY.capture.maximum_total_body_bytes) throw new MemoryCatalogError("invalid capture apply");
  }
}

export class MemoryCatalogError extends Error { constructor(message, code = "invalid_memory_request") { super(message); this.code = code; } }

export function openMemoryCatalog({ kbRoot, edgeBudget = RECALL_POLICY.expansion.maximum_related_edges, mutationHook, writeHook } = {}) {
  if (typeof kbRoot !== "string" || kbRoot.length === 0) throw new TypeError("kbRoot is required");
  if (!Number.isSafeInteger(edgeBudget) || edgeBudget < 0) throw new TypeError("edgeBudget must be a non-negative safe integer");
  return new MemoryCatalog(resolve(kbRoot), edgeBudget, mutationHook, writeHook);
}

class MemoryCatalog {
  constructor(kbRoot, edgeBudget, mutationHook, writeHook) {
    this.kbRoot = kbRoot; this.edgeBudget = edgeBudget; this.mutationHook = mutationHook; this.writeHook = writeHook; this.mutationGate = Promise.resolve(); this.flushTimer = undefined; this.retryingFlush = false; this.records = new Map(); this.stores = new Set(["default"]); this.pendingIds = new Set(); this.pendingIndexEntries = new Map(); this.indexRaw = undefined;
    this.hydrateSync();
  }
  mutate(work) { const run = async () => { await this.mutationHook?.(); return work(); }; const next = this.mutationGate.then(run, run); this.mutationGate = next.catch(() => {}); return next; }
  hydrateSync() {
    const records = discoverRecords(this.kbRoot); const seen = new Set();
    for (const record of records) { if (seen.has(record.id)) throw new MemoryCatalogError(`duplicate memory id: ${record.id}`, "duplicate_memory_id"); seen.add(record.id); }
    this.records = new Map(records.map((record) => [record.id, { ...record, raw: record.raw, persistedRaw: record.raw }]));
    this.stores = discoverStores(this.kbRoot, records); this.indexRaw = readOptional(join(this.kbRoot, "INDEX.md")); this.pendingIds.clear(); this.pendingIndexEntries.clear();
    return records.length;
  }
  async rebuild() { return this.mutate(() => { this.flushSync(); return this.hydrateSync(); }); }
  async close() { if (this.flushTimer !== undefined) { clearTimeout(this.flushTimer); this.flushTimer = undefined; } return this.mutate(() => { this.flushSync(); this.retryingFlush = false; }); }
  async lookup(input) { return this.mutate(() => this.lookupSync(input)); }
  lookupSync({ stores, terms = [] }) {
    const selected = validateStores(stores, this.stores); const literalTerms = validateTerms(terms, RECALL_POLICY.expansion.maximum_terms);
    const matched = this.ordered(selected).filter((record) => literalTerms.length === 0 || literalTerms.every((term) => `${record.title}\n${record.memory}`.toLowerCase().includes(term.toLowerCase()))).slice(0, RECALL_POLICY.candidate_limit);
    let bytes = 0; return matched.map(toRecord).filter((record) => { bytes += Buffer.byteLength(JSON.stringify(record)); return bytes <= RECALL_POLICY.candidate_bytes; });
  }
  async expand(input) { return this.mutate(() => this.expandSync(input)); }
  expandSync({ stores, ids = [], terms = [], tags = [], depth = 0 }) {
    const selected = validateStores(stores, this.stores);
    if (!Number.isSafeInteger(depth) || depth < 0 || depth > RECALL_POLICY.expansion.maximum_graph_depth) throw new MemoryCatalogError("invalid graph depth");
    const wanted = validateOptionalIds(ids, RECALL_POLICY.expansion.maximum_ids); const literalTerms = validateTerms(terms, RECALL_POLICY.expansion.maximum_terms); const requestedTags = validateTags(tags, RECALL_POLICY.expansion.maximum_tags);
    if (wanted.length === 0 && literalTerms.length === 0 && requestedTags.length === 0) throw new MemoryCatalogError("empty memory expansion");
    const all = this.ordered(selected); const byId = new Map(all.map((record) => [record.id, record])); const result = []; const add = (record) => { if (record && !result.some((item) => item.id === record.id) && result.length < RECALL_POLICY.expansion.maximum_matched_records) result.push(record); };
    for (const record of all) if (literalTerms.every((term) => `${record.title}\n${record.memory}`.toLowerCase().includes(term.toLowerCase())) && requestedTags.every((tag) => record.tags.includes(tag)) && (literalTerms.length > 0 || requestedTags.length > 0)) add(record);
    wanted.forEach((id) => add(byId.get(id)));
    let frontier = result.slice(); let edges = 0;
    graph: for (let level = 0; level < depth; level += 1) { const next = []; for (const record of frontier) for (const id of record.related) { if (++edges > this.edgeBudget) break graph; const related = byId.get(id); if (related && !result.some((item) => item.id === id)) { add(related); next.push(related); } } frontier = next; }
    result.sort(compareRecords); let bytes = 0; return result.map(toRecord).filter((record) => { bytes += Buffer.byteLength(JSON.stringify(record)); return bytes <= RECALL_POLICY.expansion.maximum_bytes; });
  }
  async apply(input) { validateApplyInput(input); return this.mutate(() => this.applySync(input)); }
  applySync({ store, operations }) {
    validateStores([store], this.stores); const known = this.records; const writes = []; const repairs = []; const batchIds = new Set(operations.map(({ id }) => id));
    for (const operation of operations) {
      const tagged = Object.hasOwn(operation, "tags"); const tags = tagged ? normalizeTags(operation.tags) : []; if (operation.related.some((id) => !known.has(id) && !batchIds.has(id))) throw new MemoryCatalogError("invalid capture operation");
      const current = known.get(operation.id);
      if (operation.mode === "create" && current) { if (current.store === store && current.tagged === tagged && current.title === operation.title && current.memory === normalizeBody(operation.body) && JSON.stringify(current.tags) === JSON.stringify(tags) && JSON.stringify(current.related) === JSON.stringify(operation.related)) { repairs.push(current); continue; } throw new MemoryCatalogError("capture conflict", "conflict"); }
      if (operation.mode === "update") {
        const expectedTags = tagged && operation.expected_tags.length > 0 ? normalizeTags(operation.expected_tags) : undefined;
        if (!current || current.store !== store || (current.tagged !== tagged && !(tagged && !current.tagged && expectedTags === undefined))) throw new MemoryCatalogError("capture conflict", "conflict");
        const expectedRaw = canonicalRecord({ id: operation.id, title: operation.expected_title, relevant_at: operation.expected_relevant_at, body: operation.expected_body, tags: expectedTags, related: operation.expected_related }); const touchedRaw = canonicalRecord({ id: operation.id, title: operation.expected_title, relevant_at: current.relevant_at, body: operation.expected_body, tags: expectedTags, related: operation.expected_related });
        if (current.raw !== expectedRaw && current.raw !== touchedRaw) throw new MemoryCatalogError("capture conflict", "conflict");
      }
      if (current && current.tagged === tagged && current.title === operation.title && current.memory === normalizeBody(operation.body) && JSON.stringify(current.tags) === JSON.stringify(tags) && JSON.stringify(current.related) === JSON.stringify(operation.related)) { repairs.push(current); continue; }
      writes.push({ operation, current, tags, tagged });
    }
    const indexPath = join(this.kbRoot, "INDEX.md"); assertSafePath(this.kbRoot, indexPath);
    for (const { operation, current } of writes) { const path = current?.path ?? recordPath(this.kbRoot, store, operation.id); assertSafePath(this.kbRoot, path); if (!current && existsSync(path)) throw new MemoryCatalogError("capture conflict", "conflict"); }
    const now = new Date().toISOString();
    for (const { operation, current, tags, tagged } of writes) {
      const path = current?.path ?? recordPath(this.kbRoot, store, operation.id); const next = { id: operation.id, title: operation.title, relevant_at: now, tags, tagged, related: operation.related, memory: normalizeBody(operation.body), store, path, persistedRaw: current?.persistedRaw, raw: canonicalRecord({ ...operation, relevant_at: now }) };
      this.records.set(next.id, next); this.pendingIds.add(next.id); if (!current) this.stores.add(store); this.pendingIndexEntries.set(indexTarget(store, next.id), next);
    }
    for (const record of repairs) this.pendingIndexEntries.set(indexTarget(record.store, record.id), record);
    if (writes.length > 0 || repairs.length > 0) this.scheduleFlush();
    return { status: writes.length ? "applied" : "noop", records: writes.length };
  }
  async touch(input) { return this.mutate(() => this.touchSync(input)); }
  touchSync({ stores, ids }) {
    const selected = validateStores(stores, this.stores); const wanted = validateIds(ids, RECALL_POLICY.expansion.maximum_ids); const matching = wanted.map((id) => this.records.get(id));
    if (matching.some((record) => !record || !selected.includes(record.store))) throw new MemoryCatalogError("memory selection is outside selected stores");
    for (const record of matching) assertSafePath(this.kbRoot, record.path);
    const now = new Date().toISOString(); for (const record of matching) { record.relevant_at = now; record.raw = canonicalFromRecord(record); this.pendingIds.add(record.id); }
    this.scheduleFlush(); return now;
  }
  ordered(selected) { return [...this.records.values()].filter((record) => selected.includes(record.store)).sort(compareRecords); }
  scheduleFlush() {
    if (this.flushTimer !== undefined) return;
    this.flushTimer = setTimeout(() => { this.flushTimer = undefined; void this.mutate(() => this.flushSync()).then(() => { this.retryingFlush = false; }, () => { if (!this.retryingFlush) { this.retryingFlush = true; this.scheduleFlush(); } }); }, FLUSH_DELAY_MS);
    this.flushTimer.unref?.();
  }
  flushSync() {
    if (this.pendingIds.size === 0 && this.pendingIndexEntries.size === 0) return;
    const records = [...this.pendingIds].map((id) => this.records.get(id)).filter(Boolean); const entries = [...this.pendingIndexEntries.values()]; const indexPath = join(this.kbRoot, "INDEX.md"); assertSafePath(this.kbRoot, indexPath);
    for (const record of records) { assertSafePath(this.kbRoot, record.path); if (readOptional(record.path) !== record.persistedRaw) throw new MemoryCatalogError("capture conflict", "conflict"); }
    const indexBefore = readOptional(indexPath); const indexAfter = updateCapturedIndexText(indexBefore ?? "", entries); if (entries.length > 0 && indexBefore !== this.indexRaw) throw new MemoryCatalogError("capture conflict", "conflict");
    const backups = []; let indexWritten = indexBefore;
    try {
      for (const record of records) { const written = canonicalFromRecord(record); backups.push([record.path, record.persistedRaw, written]); renameSync(safeTemporary(record.path, written), record.path); this.writeHook?.({ path: record.path, record }); }
      if (indexAfter !== indexBefore) { indexWritten = indexAfter; renameSync(safeTemporary(indexPath, indexAfter), indexPath); this.writeHook?.({ path: indexPath, index: true }); if (readOptional(indexPath) !== indexAfter) throw new MemoryCatalogError("capture recovery conflict", "conflict"); }
      for (const record of records) record.persistedRaw = record.raw = canonicalFromRecord(record);
      this.indexRaw = indexAfter; for (const record of records) this.pendingIds.delete(record.id); this.pendingIndexEntries.clear();
    } catch (error) {
      let recoveryConflict = false;
      for (const [path, previous, written] of backups.reverse()) { try { if (readOptional(path) !== written) { recoveryConflict = true; continue; } if (previous === undefined) unlinkSync(path); else renameSync(safeTemporary(path, previous), path); } catch { recoveryConflict = true; } }
      try { if (readOptional(indexPath) !== indexWritten) recoveryConflict = true; else if (indexBefore === undefined) { if (existsSync(indexPath)) unlinkSync(indexPath); } else { renameSync(safeTemporary(indexPath, indexBefore), indexPath); } } catch { recoveryConflict = true; }
      if (recoveryConflict) throw new MemoryCatalogError("capture recovery conflict", "conflict");
      throw error;
    }
  }
}

function assertSafePath(root, path) { const configuredRoot = resolve(root); assertLexicalRoot(configuredRoot); const absoluteRoot = realpathSync(configuredRoot); const absolutePath = join(absoluteRoot, relative(configuredRoot, resolve(path))); if (absolutePath !== absoluteRoot && !absolutePath.startsWith(`${absoluteRoot}${sep}`)) throw new MemoryCatalogError("unsafe capture path"); let current = absoluteRoot; for (let parent = dirname(current); parent !== dirname(parent); parent = dirname(parent)) if (existsSync(parent) && lstatSync(parent).isSymbolicLink()) throw new MemoryCatalogError("unsafe capture path"); if (existsSync(current) && lstatSync(current).isSymbolicLink()) throw new MemoryCatalogError("unsafe capture path"); for (const component of relative(absoluteRoot, dirname(absolutePath)).split(sep).filter(Boolean)) { current = join(current, component); if (existsSync(current) && lstatSync(current).isSymbolicLink()) throw new MemoryCatalogError("unsafe capture path"); } if (existsSync(absolutePath) && lstatSync(absolutePath).isSymbolicLink()) throw new MemoryCatalogError("unsafe capture path"); }
function assertLexicalRoot(root) { const aliases = process.platform === "darwin" ? new Map([["/tmp", "/private/tmp"], ["/var", "/private/var"], ["/etc", "/private/etc"]]) : new Map(); let current = ""; for (const component of root.split(sep).filter(Boolean)) { current = `${current}${sep}${component}`; if (!lstatSync(current).isSymbolicLink()) continue; if (aliases.get(current) !== realpathSync(current)) throw new MemoryCatalogError("unsafe capture path"); } }
function safeTemporary(path, text) { const temporary = `${path}.${randomUUID()}.tmp`; writeFileSync(temporary, text, { mode: 0o600, flag: "wx", flush: true }); return temporary; }
function recordPath(root, store, id) { return store === "default" ? join(root, `${id}.md`) : join(root, "stores", store, `${id}.md`); }
function indexTarget(store, id) { return store === "default" ? `${id}.md` : `stores/${store}/${id}.md`; }
function updateCapturedIndexText(text, entries) {
  const links = entries.map((record) => ({ target: indexTarget(record.store, record.id), text: `- [${record.title}](${indexTarget(record.store, record.id)})` })).sort((left, right) => left.target.localeCompare(right.target)); const heading = "## Automatically Captured"; const normalized = text.replace(/\n*$/, "\n"); const start = normalized.indexOf(`${heading}\n`); if (start < 0) { const missing = links.filter(({ target }) => !text.includes(`](${target})`)); return missing.length === 0 ? text : `${normalized}${heading}\n${missing.map(({ text: link }) => `${link}\n`).join("")}`; }
  const after = start + heading.length + 1; const next = normalized.slice(after).search(/^## /m); const end = next < 0 ? normalized.length : after + next; let captured = normalized.slice(after, end); const additions = [];
  for (const link of links) { const existing = new RegExp(`^- \\[[^\\n]*\\]\\(${escapeRegExp(link.target)}\\)$`, "m"); if (existing.test(captured)) captured = captured.replace(existing, link.text); else if (!normalized.includes(`](${link.target})`)) additions.push(`${link.text}\n`); }
  return `${normalized.slice(0, after)}${captured}${additions.join("")}${normalized.slice(end)}`;
}
function escapeRegExp(value) { return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }
function normalizeBody(body) { return `${body.replace(/\r\n?/g, "\n").replace(/\n*$/, "")}\n`; }
function validTags(tags, minimum = 1) { return Array.isArray(tags) && tags.length >= minimum && tags.length <= 8 && tags.every((tag) => typeof tag === "string" && TAG.test(tag) && Buffer.byteLength(tag) <= 64) && new Set(tags).size === tags.length; }
function normalizeTags(tags) { return [...tags].sort((left, right) => Buffer.compare(Buffer.from(left), Buffer.from(right))); }
function sortedTags(tags) { return tags.every((tag, index) => index === 0 || Buffer.compare(Buffer.from(tags[index - 1]), Buffer.from(tag)) < 0); }
function validProseBody(body) { if (typeof body !== "string") return false; const normalized = normalizeBody(body).slice(0, -1); if (normalized.length === 0 || normalized !== normalized.trim()) return false; const paragraphs = normalized.split(/\n[ \t]*\n/); return paragraphs.length >= 1 && paragraphs.length <= 3 && paragraphs.every((paragraph) => paragraph.split("\n").every((line) => line.trim().length > 0 && !/^(?:#{1,6}\s|>|[-*+]\s|\d+[.)]\s|```)/.test(line))); }
function canonicalRecord({ id, title, relevant_at, tags, related, memory, body }) { return `---\nid: ${id}\ntitle: ${title}\nrelevant_at: ${relevant_at}\n${tags === undefined ? "" : `tags:\n${normalizeTags(tags).map((value) => `  - ${value}\n`).join("")}`}related:\n${related.map((value) => `  - ${value}\n`).join("")}---\n\n${normalizeBody(body ?? memory)}`; }
function canonicalFromRecord(record) { return canonicalRecord({ ...record, tags: record.tagged ? record.tags : undefined }); }
function discoverRecords(root) { if (!existsSync(root)) return []; const files = []; const walk = (directory, store) => { for (const entry of readdirSync(directory, { withFileTypes: true })) { const path = join(directory, entry.name); if (entry.isDirectory()) walk(path, store); else if (entry.isFile() && entry.name.endsWith(".md") && entry.name !== "INDEX.md") files.push([path, store]); } }; for (const entry of readdirSync(root, { withFileTypes: true })) { const path = join(root, entry.name); if (entry.isFile() && entry.name.endsWith(".md") && entry.name !== "INDEX.md") files.push([path, "default"]); if (entry.isDirectory() && entry.name === "stores") for (const store of readdirSync(path, { withFileTypes: true })) if (store.isDirectory() && STORE_ID.test(store.name)) walk(join(path, store.name), store.name); } return files.map(([path, store]) => parseRecord(path, store)).filter(Boolean); }
function discoverStores(root, records) { const stores = new Set(["default", ...records.map(({ store }) => store)]); const directory = join(root, "stores"); if (existsSync(directory)) for (const entry of readdirSync(directory, { withFileTypes: true })) if (entry.isDirectory() && STORE_ID.test(entry.name)) stores.add(entry.name); return stores; }
function parseRecord(path, store) { const text = readFileSync(path, "utf8"); if (text.includes("\r") || !text.endsWith("\n") || text.endsWith("\n\n")) return undefined; const match = /^---\n([\s\S]*?)\n---\n\n([\s\S]*)$/.exec(text); if (!match) return undefined; const lines = match[1].split("\n"); const tagged = lines[3] === "tags:"; const keys = lines.filter((line) => /^[a-z_]+:/.test(line)).map((line) => line.split(":", 1)[0]); if (keys.join(",") !== (tagged ? "id,title,relevant_at,tags,related" : "id,title,relevant_at,related")) return undefined; const id = value(lines[0]); const title = value(lines[1]); const relevant_at = value(lines[2]); if (lines[0] !== `id: ${id}` || lines[1] !== `title: ${title}` || lines[2] !== `relevant_at: ${relevant_at}`) return undefined; const relatedAt = tagged ? lines.indexOf("related:", 4) : 3; if (relatedAt < 0 || lines[relatedAt] !== "related:" || lines.slice(tagged ? 4 : 3, relatedAt).some((line) => !/^  - .+$/.test(line)) || lines.slice(relatedAt + 1).some((line) => !/^  - [a-z0-9][a-z0-9-]{0,127}$/.test(line))) return undefined; const tags = tagged ? lines.slice(4, relatedAt).map((line) => line.slice(4)) : []; const related = lines.slice(relatedAt + 1).map((line) => line.slice(4)); if (!ID.test(id) || !title.trim() || title !== title.trim() || Number.isNaN(Date.parse(relevant_at)) || new Date(relevant_at).toISOString() !== relevant_at || !validTags(tags, tagged ? 1 : 0) || (tagged && !sortedTags(tags)) || related.some((item) => !ID.test(item)) || (tagged && !validProseBody(match[2]))) return undefined; return { id, title, relevant_at: new Date(relevant_at).toISOString(), tags, tagged, related, memory: match[2], store, path, raw: text }; }
function value(line) { return line.slice(line.indexOf(":") + 1).trim(); }
function toRecord(record) { return { id: record.id, title: record.title, memory: boundUtf8(record.memory, 8192), relevant_at: record.relevant_at, tags: record.tags, related: record.related, store: record.store }; }
function compareRecords(left, right) { return right.relevant_at.localeCompare(left.relevant_at) || left.id.localeCompare(right.id); }
function boundUtf8(value, maximum) { let bytes = 0; let output = ""; for (const character of value) { const size = Buffer.byteLength(character); if (bytes + size > maximum) break; output += character; bytes += size; } return output; }
function validateStores(stores, available) { if (!Array.isArray(stores) || stores.length === 0 || new Set(stores).size !== stores.length || stores.some((store) => store !== "default" && !STORE_ID.test(store)) || stores.some((store) => !available.has(store))) throw new MemoryCatalogError("invalid selected stores"); return stores; }
function validateIds(ids, maximum) { if (!Array.isArray(ids) || ids.length === 0 || ids.length > maximum || new Set(ids).size !== ids.length || ids.some((id) => !ID.test(id))) throw new MemoryCatalogError("invalid memory ids"); return ids; }
function validateOptionalIds(ids, maximum) { if (!Array.isArray(ids) || ids.length > maximum || new Set(ids).size !== ids.length || ids.some((id) => !ID.test(id))) throw new MemoryCatalogError("invalid memory ids"); return ids; }
function validateTerms(terms, maximum) { if (!Array.isArray(terms) || terms.length > maximum || terms.some((term) => typeof term !== "string" || term.length === 0 || term.length > 128)) throw new MemoryCatalogError("invalid literal terms"); return terms; }
function validateTags(tags, maximum) { if (!Array.isArray(tags) || tags.length > maximum || new Set(tags).size !== tags.length || tags.some((tag) => typeof tag !== "string" || !TAG.test(tag) || Buffer.byteLength(tag) > 64)) throw new MemoryCatalogError("invalid memory tags"); return tags; }
function readOptional(path) { return existsSync(path) ? readFileSync(path, "utf8") : undefined; }
