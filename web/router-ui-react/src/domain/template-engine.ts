const TEMPLATE_TOKEN_REGEX = /\$\{([a-zA-Z0-9_-]+)\}|\{\{\s*([a-zA-Z0-9_-]+)\s*\}\}/g;
const TEMPLATE_INCLUSION_REGEX = /\{\{\s*\.\s*([a-zA-Z0-9_-]+)\s*\}\}/g;
const MAX_TEMPLATE_DEPTH = 10;

function getTemplateBody(
  id: string,
  templates?: Record<string, string> | Map<string, string>,
): string | undefined {
  if (!templates) return undefined;
  if (templates instanceof Map) {
    return templates.get(id);
  }
  return Object.hasOwn(templates, id) ? templates[id] : undefined;
}

export function parseTemplateTokens(
  body: unknown,
  templates?: Record<string, string> | Map<string, string>,
): string[] {
  const names: string[] = [];
  const seenTokens = new Set<string>();
  const visitedTemplates = new Set<string>();

  function collect(inputBody: unknown) {
    const input = String(inputBody ?? "");

    // Extract regular variable tokens
    for (const match of input.matchAll(TEMPLATE_TOKEN_REGEX)) {
      const name = match[1] ?? match[2];
      if (name !== undefined && !seenTokens.has(name)) {
        seenTokens.add(name);
        names.push(name);
      }
    }

    // Recursively extract tokens from included templates
    if (templates) {
      for (const match of input.matchAll(TEMPLATE_INCLUSION_REGEX)) {
        const templateId = match[1];
        if (templateId && !visitedTemplates.has(templateId)) {
          visitedTemplates.add(templateId);
          const includedBody = getTemplateBody(templateId, templates);
          if (includedBody !== undefined) {
            collect(includedBody);
          }
        }
      }
    }
  }

  collect(body);
  return names;
}

function expandTemplates(
  text: string,
  values: Record<string, unknown>,
  templates?: Record<string, string> | Map<string, string>,
  stack: string[] = [],
  depth = 0,
): string {
  if (!text) {
    return "";
  }

  const expanded = text.replace(TEMPLATE_INCLUSION_REGEX, (_match, templateId: string) => {
    if (depth >= MAX_TEMPLATE_DEPTH) {
      return "[Max template depth exceeded]";
    }

    if (stack.includes(templateId)) {
      const cyclePath = [...stack, templateId].join(" -> ");
      return `[Template cycle detected: ${cyclePath}]`;
    }

    const templateContent = getTemplateBody(templateId, templates);
    if (templateContent === undefined) {
      return `[Missing template: ${templateId}]`;
    }

    return expandTemplates(templateContent, values, templates, [...stack, templateId], depth + 1);
  });

  return expanded.replace(TEMPLATE_TOKEN_REGEX, (match, p1: string | undefined, p2: string | undefined) => {
    const name = p1 ?? p2;
    if (name !== undefined && Object.hasOwn(values, name)) {
      return String(values[name] ?? "");
    }
    return match;
  });
}

export function renderTemplate(
  template: unknown,
  values: Record<string, unknown> = {},
  templates?: Record<string, string> | Map<string, string>,
): string {
  const input = String(template ?? "");
  return expandTemplates(input, values, templates, [], 0);
}

