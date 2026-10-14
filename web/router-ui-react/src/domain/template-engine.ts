const TEMPLATE_TOKEN_REGEX = /\$\{([a-zA-Z0-9_-]+)\}|\{\{\s*([a-zA-Z0-9_-]+)\s*\}\}/g;

export function parseTemplateTokens(body: unknown): string[] {
  const names: string[] = [];
  const seen = new Set<string>();
  const input = String(body ?? "");
  for (const match of input.matchAll(TEMPLATE_TOKEN_REGEX)) {
    const name = match[1] ?? match[2];
    if (name !== undefined && !seen.has(name)) {
      seen.add(name);
      names.push(name);
    }
  }
  return names;
}

export function renderTemplate(template: unknown, values: Record<string, unknown> = {}): string {
  const input = String(template ?? "");
  return input.replace(TEMPLATE_TOKEN_REGEX, (match, p1: string | undefined, p2: string | undefined) => {
    const name = p1 ?? p2;
    if (name !== undefined && Object.hasOwn(values, name)) {
      return String(values[name] ?? "");
    }
    return match;
  });
}
