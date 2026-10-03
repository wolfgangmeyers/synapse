const MAX_LINE_BYTES = 64 * 1024;
const MAX_RESULT_BYTES = 128 * 1024;
const MAX_RESPONSE_BYTES = 8 * 1024;
const MAX_MODELS = 256;
const PROVIDERS = new Set(["openai-codex", "google", "cursor", "google-vertex", "antigravity"]);
const OPERATIONS = new Set(["models", "login", "logout"]);

function hasLoneSurrogate(value) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) return true;
  }
  return false;
}

function isRecord(value) { return value !== null && typeof value === "object" && !Array.isArray(value); }
function safeString(value, max, allowEmpty = false) {
  return typeof value === "string" && (allowEmpty ? value.length >= 0 : value.length > 0) && value.length <= max && !hasLoneSurrogate(value);
}

function rejectDuplicateObjectKeys(line) {
  let index = 0;
  const whitespace = () => { while (/\s/u.test(line[index] ?? "")) index += 1; };
  const stringEnd = () => {
    const start = index;
    index += 1;
    while (index < line.length) {
      if (line[index] === "\\") { index += 2; continue; }
      if (line[index] === '"') { index += 1; return line.slice(start, index); }
      index += 1;
    }
    throw new Error("invalid string");
  };
  const value = () => {
    whitespace();
    if (line[index] === '"') { stringEnd(); return; }
    if (line[index] === "{") { object(); return; }
    if (line[index] === "[") { array(); return; }
    while (index < line.length && !",]}".includes(line[index] ?? "") && !/\s/u.test(line[index] ?? "")) index += 1;
  };
  const object = () => {
    index += 1; whitespace();
    const keys = new Set();
    if (line[index] === "}") { index += 1; return; }
    while (index < line.length) {
      whitespace();
      if (line[index] !== '"') throw new Error("invalid object key");
      const key = JSON.parse(stringEnd());
      if (keys.has(key)) throw new Error("duplicate object key");
      keys.add(key); whitespace();
      if (line[index] !== ":") throw new Error("invalid object");
      index += 1; value(); whitespace();
      if (line[index] === "}") { index += 1; return; }
      if (line[index] !== ",") throw new Error("invalid object");
      index += 1;
    }
    throw new Error("incomplete object");
  };
  const array = () => {
    index += 1; whitespace();
    if (line[index] === "]") { index += 1; return; }
    while (index < line.length) {
      value(); whitespace();
      if (line[index] === "]") { index += 1; return; }
      if (line[index] !== ",") throw new Error("invalid array");
      index += 1;
    }
    throw new Error("incomplete array");
  };
  value(); whitespace();
  if (index !== line.length) throw new Error("trailing data");
}

function parseJSON(line, max) {
  if (typeof line !== "string" || Buffer.byteLength(line, "utf8") > max) throw new Error("line too large");
  try { rejectDuplicateObjectKeys(line); return JSON.parse(line); } catch { throw new Error("invalid JSON"); }
}

export function parseRequest(line) {
  const value = parseJSON(line, MAX_LINE_BYTES);
  if (!isRecord(value) || !OPERATIONS.has(value.operation)) throw new Error("unsupported operation");
  // The catalog-only request is retained for Phase 3 callers. Provider auth
  // operations require a router-generated ID and one fixed provider name.
  if (value.operation === "models") {
    if (Object.keys(value).length !== 1) throw new Error("invalid request");
    return value;
  }
  if (Object.keys(value).length !== 3 || !safeString(value.operation_id, 128) || !PROVIDERS.has(value.provider)) throw new Error("invalid request");
  return value;
}

export function parseResponse(line) {
  const value = parseJSON(line, MAX_RESPONSE_BYTES);
  if (!isRecord(value) || Object.keys(value).length !== 3 || value.type !== "response" || !safeString(value.prompt_id, 128) || !safeString(value.value, MAX_RESPONSE_BYTES, true)) throw new Error("invalid response");
  return value;
}

function safeModel(value) {
  if (!isRecord(value) || !PROVIDERS.has(value.provider) || !safeString(value.id, 128) || !safeString(value.name, 512)) return undefined;
  return { provider: value.provider, id: value.id, name: value.name, available: value.available === true };
}

export function projectCatalog(models) {
  if (!Array.isArray(models)) throw new Error("model catalog is invalid");
  const projected = [];
  const seen = new Set();
  for (const value of models) {
    const model = safeModel(value);
    if (!model || !model.available) continue;
    const key = `${model.provider}\u0000${model.id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    projected.push(model);
    if (projected.length > MAX_MODELS) throw new Error("model catalog is too large");
  }
  projected.sort((left, right) => `${left.provider}\u0000${left.id}`.localeCompare(`${right.provider}\u0000${right.id}`));
  return { models: projected };
}

function safeProviderStatus(value) {
  if (!isRecord(value) || !PROVIDERS.has(value.provider) || !safeString(value.label, 128) || !safeString(value.status, 64) || typeof value.authenticated !== "boolean") return undefined;
  const result = { provider: value.provider, label: value.label, authenticated: value.authenticated, status: value.status };
  if (typeof value.account_label === "string" && value.account_label.length <= 256 && !hasLoneSurrogate(value.account_label)) result.account_label = value.account_label;
  return result;
}

export function terminalResult(request, status, snapshot, error) {
  const result = { type: "terminal", operation_id: request.operation_id, provider: request.provider, status };
  if (status === "succeeded") {
    const statuses = (snapshot?.providers ?? []).map(safeProviderStatus);
    if (statuses.length !== 5 || statuses.some((item) => item === undefined) || new Set(statuses.map((item) => item.provider)).size !== 5) throw new Error("provider snapshot is invalid");
    result.providers = statuses;
    result.models = projectCatalog(snapshot?.models ?? []).models;
  } else {
    result.error = typeof error === "string" && error.length > 0 && error.length <= 512 ? error : "Provider sign-in did not complete";
  }
  return result;
}

export function promptEvent(request, prompt) {
  if (!safeString(prompt.id, 128) || !safeString(prompt.kind, 32) || !safeString(prompt.message, 512)) throw new Error("invalid prompt");
  const result = { type: "prompt", operation_id: request.operation_id, provider: request.provider, prompt: { id: prompt.id, kind: prompt.kind, message: prompt.message } };
  if (typeof prompt.placeholder === "string" && prompt.placeholder.length <= 256 && !hasLoneSurrogate(prompt.placeholder)) result.prompt.placeholder = prompt.placeholder;
  if (typeof prompt.url === "string" && prompt.url.length <= 2048 && !hasLoneSurrogate(prompt.url)) result.prompt.url = prompt.url;
  return result;
}

export function encodeResult(value) {
  const line = JSON.stringify(value);
  if (Buffer.byteLength(line, "utf8") > MAX_RESULT_BYTES) throw new Error("helper result is too large");
  return `${line}\n`;
}

export function safeError(request) {
  if (request?.operation_id && request?.provider) return terminalResult(request, "failed", undefined, "Provider sign-in did not complete");
  // The Phase 3 catalog helper shape stays intentionally generic.
  return { error: "model catalog unavailable" };
}
