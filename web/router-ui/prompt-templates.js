const PROMPT_TOKEN = /\$\{([^{}]+)\}/g;
const PROMPT_TEMPLATE_EXPORT_FORMAT = "mecha-prompt-templates";
const PROMPT_TEMPLATE_EXPORT_VERSION = 1;
const MAX_PROMPT_TEMPLATES = 100;
const MAX_PROMPT_TEMPLATE_NAME_BYTES = 128;
const MAX_PROMPT_TEMPLATE_BODY_BYTES = 64 * 1024;
const MAX_PROMPT_TEMPLATE_ID_BYTES = 1024;
const CANONICAL_PROMPT_TEMPLATE_TIMESTAMP = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const PROMPT_TEMPLATE_IMPORT_KEYS = new Set(["name", "body", "id", "updated_at"]);
const textEncoder = new TextEncoder();

/** Prompt-template IDs must contain only complete UTF-16 code points for UTF-8 portability. */
export function isPortablePromptTemplateId(value) {
  if (typeof value !== "string" || value.length === 0 || value.includes("\u0000")) return false;
  for (let index = 0; index < value.length; index += 1) {
    const codeUnit = value.charCodeAt(index);
    if (codeUnit >= 0xD800 && codeUnit <= 0xDBFF) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xDC00 && next <= 0xDFFF)) return false;
      index += 1;
    } else if (codeUnit >= 0xDC00 && codeUnit <= 0xDFFF) {
      return false;
    }
  }
  return textEncoder.encode(value).length <= MAX_PROMPT_TEMPLATE_ID_BYTES;
}

function isCanonicalPromptTemplateTimestamp(value) {
  if (typeof value !== "string" || !CANONICAL_PROMPT_TEMPLATE_TIMESTAMP.test(value)) return false;
  try {
    return new Date(value).toISOString() === value;
  } catch {
    return false;
  }
}

/** Return a portable JSON shape that can be imported through the existing CRUD paths. */
export function promptTemplateExportPayload(templates) {
  return {
    format: PROMPT_TEMPLATE_EXPORT_FORMAT,
    version: PROMPT_TEMPLATE_EXPORT_VERSION,
    templates: (Array.isArray(templates) ? templates : []).map((template) => {
      const exported = {
        name: String(template?.name ?? ""),
        body: String(template?.body ?? ""),
      };
      if (template && typeof template === "object" && Object.hasOwn(template, "id")) {
        exported.id = template.id;
      }
      if (template && typeof template === "object" && isCanonicalPromptTemplateTimestamp(template.updated_at)) {
        exported.updated_at = template.updated_at;
      }
      return exported;
    }),
  };
}

/** Parse and validate a prompt-template export before any import request is sent. */
export function parsePromptTemplateImport(value) {
  let parsed;
  try {
    parsed = JSON.parse(String(value));
  } catch {
    throw new Error("Choose a valid prompt template JSON file.");
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)
    || parsed.format !== PROMPT_TEMPLATE_EXPORT_FORMAT
    || parsed.version !== PROMPT_TEMPLATE_EXPORT_VERSION
    || !Array.isArray(parsed.templates)
    || parsed.templates.length > MAX_PROMPT_TEMPLATES) {
    throw new Error("This is not a valid prompt template export file.");
  }
  const names = new Set();
  const ids = new Set();
  return parsed.templates.map((template, index) => {
    if (!template || typeof template !== "object" || Array.isArray(template)
      || Object.keys(template).some((key) => !PROMPT_TEMPLATE_IMPORT_KEYS.has(key))) {
      throw new Error(`Template ${index + 1} has invalid metadata.`);
    }
    if (typeof template.name !== "string" || typeof template.body !== "string"
      || template.name.includes("\u0000") || template.body.includes("\u0000")
      || template.name.trim() === "" || template.body.trim() === ""
      || textEncoder.encode(template.name).length > MAX_PROMPT_TEMPLATE_NAME_BYTES
      || textEncoder.encode(template.body).length > MAX_PROMPT_TEMPLATE_BODY_BYTES) {
      throw new Error(`Template ${index + 1} must have a valid name and body.`);
    }
    if (Object.hasOwn(template, "id")) {
      if (!isPortablePromptTemplateId(template.id)) {
        throw new Error(`Template ${index + 1} has invalid metadata.`);
      }
      if (ids.has(template.id)) throw new Error(`The import contains duplicate template id \"${template.id}\".`);
      ids.add(template.id);
    }
    if (Object.hasOwn(template, "updated_at") && !isCanonicalPromptTemplateTimestamp(template.updated_at)) {
      throw new Error(`Template ${index + 1} has invalid metadata.`);
    }
    if (names.has(template.name)) throw new Error(`The import contains duplicate template name \"${template.name}\".`);
    names.add(template.name);
    return {
      ...(Object.hasOwn(template, "id") ? { id: template.id } : {}),
      name: template.name,
      body: template.body,
      ...(Object.hasOwn(template, "updated_at") ? { updated_at: template.updated_at } : {}),
    };
  });
}

/** Return distinct, non-empty prompt token names in their first-use order. */
export function parsePromptTemplateTokens(body) {
  const names = [];
  const seen = new Set();
  for (const match of String(body ?? "").matchAll(PROMPT_TOKEN)) {
    const name = match[1];
    if (!seen.has(name)) {
      seen.add(name);
      names.push(name);
    }
  }
  return names;
}

/** Replace prompt tokens with literal values, leaving unknown or malformed text intact. */
export function renderPromptTemplate(body, values = {}) {
  return String(body ?? "").replace(PROMPT_TOKEN, (token, name) => (
    Object.hasOwn(values, name) ? String(values[name] ?? "") : token
  ));
}
