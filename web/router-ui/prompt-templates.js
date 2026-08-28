const PROMPT_TOKEN = /\$\{([^{}]+)\}/g;
const PROMPT_TEMPLATE_EXPORT_FORMAT = "mecha-prompt-templates";
const PROMPT_TEMPLATE_EXPORT_VERSION = 1;
const MAX_PROMPT_TEMPLATES = 100;
const MAX_PROMPT_TEMPLATE_NAME_BYTES = 128;
const MAX_PROMPT_TEMPLATE_BODY_BYTES = 64 * 1024;

/** Return a portable JSON shape that can be imported through the existing CRUD paths. */
export function promptTemplateExportPayload(templates) {
  return {
    format: PROMPT_TEMPLATE_EXPORT_FORMAT,
    version: PROMPT_TEMPLATE_EXPORT_VERSION,
    templates: (Array.isArray(templates) ? templates : []).map((template) => ({
      name: String(template?.name ?? ""),
      body: String(template?.body ?? ""),
    })),
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
  return parsed.templates.map((template, index) => {
    if (!template || typeof template !== "object" || Array.isArray(template)
      || typeof template.name !== "string" || typeof template.body !== "string"
      || template.name.includes("\u0000") || template.body.includes("\u0000")
      || template.name.trim() === "" || template.body.trim() === ""
      || new TextEncoder().encode(template.name).length > MAX_PROMPT_TEMPLATE_NAME_BYTES
      || new TextEncoder().encode(template.body).length > MAX_PROMPT_TEMPLATE_BODY_BYTES) {
      throw new Error(`Template ${index + 1} must have a valid name and body.`);
    }
    if (names.has(template.name)) throw new Error(`The import contains duplicate template name \"${template.name}\".`);
    names.add(template.name);
    return { name: template.name, body: template.body };
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
