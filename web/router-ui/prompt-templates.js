const PROMPT_TOKEN = /\$\{([^{}]+)\}/g;

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
