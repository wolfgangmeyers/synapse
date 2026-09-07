const MAX_LINE_BYTES = 64 * 1024;
const MAX_RESULT_BYTES = 128 * 1024;

function hasLoneSurrogate(value) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      index += 1;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function rejectDuplicateObjectKeys(line) {
  let index = 0;
  const whitespace = () => { while (/\\s/u.test(line[index] ?? "")) index += 1; };
  const stringEnd = () => {
    const start = index;
    index += 1;
    while (index < line.length) {
      if (line[index] === "\\\\") { index += 2; continue; }
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
    while (index < line.length && !",]}".includes(line[index] ?? "") && !/\\s/u.test(line[index] ?? "")) index += 1;
  };
  const object = () => {
    index += 1;
    whitespace();
    const keys = new Set();
    if (line[index] === "}") { index += 1; return; }
    while (index < line.length) {
      whitespace();
      if (line[index] !== '"') throw new Error("invalid object key");
      const key = JSON.parse(stringEnd());
      if (keys.has(key)) throw new Error("duplicate object key");
      keys.add(key);
      whitespace();
      if (line[index] !== ":") throw new Error("invalid object");
      index += 1;
      value();
      whitespace();
      if (line[index] === "}") { index += 1; return; }
      if (line[index] !== ",") throw new Error("invalid object");
      index += 1;
    }
    throw new Error("incomplete object");
  };
  const array = () => {
    index += 1;
    whitespace();
    if (line[index] === "]") { index += 1; return; }
    while (index < line.length) {
      value();
      whitespace();
      if (line[index] === "]") { index += 1; return; }
      if (line[index] !== ",") throw new Error("invalid array");
      index += 1;
    }
    throw new Error("incomplete array");
  };
  value();
}

export function parseRequest(line) {
  if (typeof line !== "string" || Buffer.byteLength(line, "utf8") > MAX_LINE_BYTES) throw new Error("request is too large");
  let value;
  try {
    value = JSON.parse(line);
    rejectDuplicateObjectKeys(line);
  } catch {
    throw new Error("request is invalid JSON");
  }
  if (!isRecord(value) || Object.keys(value).length !== 1 || value.operation !== "models") throw new Error("unsupported operation");
  return value;
}

function safeModel(value) {
  if (!isRecord(value) || typeof value.provider !== "string" || typeof value.id !== "string" || typeof value.name !== "string") return undefined;
  if (value.provider.length === 0 || value.id.length === 0 || value.provider.length > 128 || value.id.length > 128 || value.name.length > 512) return undefined;
  if (hasLoneSurrogate(value.provider) || hasLoneSurrogate(value.id) || hasLoneSurrogate(value.name)) return undefined;
  return { provider: value.provider, id: value.id, name: value.name, available: true };
}

export function projectCatalog(models) {
  if (!Array.isArray(models)) throw new Error("model catalog is invalid");
  const projected = [];
  const seen = new Set();
  for (const value of models) {
    const model = safeModel(value);
    if (!model || value.available !== true) continue;
    const key = `${model.provider}\u0000${model.id}`;
    if (seen.has(key)) continue;
    seen.add(key);
    projected.push(model);
  }
  projected.sort((left, right) => `${left.provider}\u0000${left.id}`.localeCompare(`${right.provider}\u0000${right.id}`));
  return { models: projected };
}

export function encodeResult(value) {
  const line = JSON.stringify(value);
  if (Buffer.byteLength(line, "utf8") > MAX_RESULT_BYTES) throw new Error("model catalog result is too large");
  return `${line}\n`;
}

export function safeError() {
  // Do not expose provider, filesystem, auth, or SDK error text to the router.
  return { error: "model catalog unavailable" };
}
