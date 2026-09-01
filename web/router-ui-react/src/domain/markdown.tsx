import * as React from "react";
import MarkdownIt from "markdown-it";

const ALLOWED = new Set(["http:", "https:", "mailto:"]);
export function isAllowedMarkdownLink(value: unknown): boolean {
  if (typeof value !== "string" || value.trim() !== value || value.length === 0) return false;
  try {
    const url = new URL(value);
    if (!ALLOWED.has(url.protocol)) return false;
    if (url.protocol === "mailto:") return /^mailto:[^\s/]+$/i.test(value);
    return value.startsWith(`${url.protocol}//`);
  } catch { return false; }
}
function normalizeLabel(label: string): string { return label.trim().replace(/\s+/g, " ").toLowerCase(); }
function definitionLabel(line: string): string { const match = /^ {0,3}\[([^\]]+)\]:/.exec(line); return match ? normalizeLabel(match[1] ?? "") : ""; }
export function textWithoutMarkdownImages(body: unknown): string {
  const labels = new Set<string>();
  const inline = String(body ?? "").replace(/!\[[^\]\n]*\]\([^\)\n]*(?:\s+"[^"\n]*")?\)/g, "");
  const full = inline.replace(/!\[([^\]\n]*)\]\[([^\]\n]*)\]/g, (_match, alt: string, label: string) => { labels.add(normalizeLabel(label || alt)); return ""; });
  const bare = full.replace(/!\[([^\]\n]+)\](?![\[(])/g, (_match, alt: string) => { labels.add(normalizeLabel(alt)); return ""; });
  return labels.size === 0 ? bare : bare.split("\n").filter((line) => !labels.has(definitionLabel(line))).join("\n");
}

function escapeHtml(value: string): string { return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/\"/g, "&quot;").replace(/'/g, "&#39;"); }
function extractFallbackCodeBlocks(body: string, blocks: string[]): string {
  const lines = body.split("\n");
  const output: string[] = [];
  for (let index = 0; index < lines.length;) {
    const line = lines[index] ?? "";
    if (!/^\s*```[^\n]*$/.test(line) || index === lines.length - 1) {
      output.push(line);
      index += 1;
      continue;
    }
    let end = index + 1;
    while (end < lines.length && !/^\s*```\s*$/.test(lines[end] ?? "")) end += 1;
    const code = lines.slice(index + 1, end).join("\n").replace(/\n$/, "");
    const token = `\u0000BLOCK${blocks.length}\u0000`;
    blocks.push(`<pre><code>${escapeHtml(code)}</code></pre>`);
    output.push(token);
    index = end < lines.length ? end + 1 : lines.length;
  }
  return output.join("\n");
}
function replaceFallbackMarkdownLinks(value: string, links: string[]): string {
  const pattern = /(!?)\[([^\]\n]+)\]\(/g;
  let output = "";
  let cursor = 0;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(value)) !== null) {
    if (match[1] === "!" || (match.index > 0 && value[match.index - 1] === "\\")) continue;
    const destinationStart = pattern.lastIndex;
    let destinationEnd = destinationStart;
    if (value[destinationStart] === "<") {
      const bracketEnd = value.indexOf(">", destinationStart + 1);
      if (bracketEnd < 0 || value[bracketEnd + 1] !== ")") continue;
      destinationEnd = bracketEnd + 1;
    } else {
      let parentheses = 0;
      while (destinationEnd < value.length) {
        const character = value[destinationEnd];
        if (character === "(") parentheses += 1;
        if (character === ")") {
          if (parentheses === 0) break;
          parentheses -= 1;
        }
        destinationEnd += 1;
      }
      if (destinationEnd === value.length || value[destinationEnd] !== ")" || parentheses !== 0) continue;
    }
    const label = match[2] ?? "";
    const href = value.slice(destinationStart, destinationEnd);
    const token = `\u0000LINK${links.length}\u0000`;
    links.push(isAllowedMarkdownLink(href.replace(/&amp;/g, "&")) ? `<a target="_blank" rel="noopener noreferrer" href="${href}">${label}</a>` : value.slice(match.index, destinationEnd + 1));
    output += `${value.slice(cursor, match.index)}${token}`;
    cursor = destinationEnd + 1;
    pattern.lastIndex = cursor;
  }
  return `${output}${value.slice(cursor)}`;
}
function fallbackMarkdown(body: string): string {
  const blocks: string[] = [];
  const source = extractFallbackCodeBlocks(body, blocks);
  const inlineCode: string[] = [];
  const links: string[] = [];
  const lines = source.split("\n").map((line) => {
    let html = escapeHtml(line);
    html = html.replace(/`([^`\n]+)`/g, (_match, value: string) => { const token = `\u0000CODE${inlineCode.length}\u0000`; inlineCode.push(`<code>${escapeHtml(value)}</code>`); return token; });
    html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    html = replaceFallbackMarkdownLinks(html, links);
    return html;
  });
  let html = lines.join("<br>");
  for (const [index, value] of links.entries()) html = html.replace(`\u0000LINK${index}\u0000`, value);
  for (const [index, value] of inlineCode.entries()) html = html.replace(`\u0000CODE${index}\u0000`, value);
  for (const [index, value] of blocks.entries()) html = html.replace(`\u0000BLOCK${index}\u0000`, value);
  return html;
}
interface MarkdownToken { content: string; attrGet(name: string): string | null }
interface Linkify { set(options: { fuzzyLink: boolean; fuzzyEmail: boolean }): Linkify; add(schema: string, definition: null): Linkify }
interface MarkdownRendererSelf { utils: { escapeHtml(value: string): string } }
type MarkdownRendererRule = (tokens: MarkdownToken[], index: number, options: unknown, env: unknown, self: MarkdownRendererSelf) => string;
interface MarkdownRenderer { validateLink: (value: string) => boolean; render: (value: string) => string; linkify?: Linkify; renderer?: { rules: Record<string, MarkdownRendererRule> } }
type MarkdownConstructor = new (options: { html: boolean; breaks: boolean; linkify: boolean }) => MarkdownRenderer;
function isMarkdownConstructor(value: unknown): value is MarkdownConstructor { return typeof value === "function"; }
function createMarkdownRenderer(MarkdownItCtor: MarkdownConstructor): MarkdownRenderer {
  const renderer = new MarkdownItCtor({ html: false, breaks: true, linkify: true });
  renderer.validateLink = isAllowedMarkdownLink;
  renderer.linkify?.set({ fuzzyLink: false, fuzzyEmail: false }).add("ftp:", null).add("//", null).add("mailto:", null);
  if (renderer.renderer !== undefined) {
    renderer.renderer.rules.image = (tokens, index) => {
      const token = tokens[index];
      const alt = escapeHtml(token?.content ?? "");
      const src = escapeHtml(token?.attrGet("src") ?? "");
      return `![${alt}](${src})`;
    };
  }
  return renderer;
}
export function markdownToSafeHtml(body: unknown, MarkdownItCtor?: unknown): string {
  const source = String(body ?? "");
  const ctor = MarkdownItCtor === undefined ? MarkdownIt : MarkdownItCtor;
  if (isMarkdownConstructor(ctor)) {
    const renderer = createMarkdownRenderer(ctor);
    return renderer.render(source).replace(/<a href=/g, '<a target="_blank" rel="noopener noreferrer" href=').replace(/\n$/, "");
  }
  // Fallback is selected only when tests inject a non-constructor. Production bundles MarkdownIt and does not use this path to recover from a missing or failed runtime.
  return fallbackMarkdown(source);
}

/** Markdown-it disables raw HTML and validates rendered links before this component inserts the markup. */
export function markdownToReact(body: unknown): React.ReactElement {
  return React.createElement("div", { className: "markdown-body", dangerouslySetInnerHTML: { __html: markdownToSafeHtml(body) } });
}
export const MarkdownBody = React.memo(({ body }: { body: string }): React.ReactElement => markdownToReact(body));
