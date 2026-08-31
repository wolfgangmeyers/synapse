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
function fallbackMarkdown(body: string): string {
  const blocks: string[] = [];
  const source = body.replace(/^\s*```[^\n]*\n([\s\S]*?)^\s*```\s*$/gm, (_match, code: string) => { const token = `\u0000BLOCK${blocks.length}\u0000`; blocks.push(`<pre><code>${escapeHtml(code.replace(/\n$/, ""))}</code></pre>`); return token; });
  const inlineCode: string[] = [];
  const lines = source.split("\n").map((line) => {
    let html = escapeHtml(line);
    html = html.replace(/`([^`\n]+)`/g, (_match, value: string) => { const token = `\u0000CODE${inlineCode.length}\u0000`; inlineCode.push(`<code>${escapeHtml(value)}</code>`); return token; });
    html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
    html = html.replace(/\[([^\]]+)\]\(([^)\n]+)\)/g, (match: string, label: string, href: string) => isAllowedMarkdownLink(href) ? `<a target="_blank" rel="noopener noreferrer" href="${escapeHtml(href)}">${label}</a>` : match);
    return html;
  });
  let html = lines.join("<br>");
  for (const [index, value] of inlineCode.entries()) html = html.replace(`\u0000CODE${index}\u0000`, value);
  for (const [index, value] of blocks.entries()) html = html.replace(`\u0000BLOCK${index}\u0000`, value);
  return html;
}
interface MarkdownToken { content: string; attrGet(name: string): string | null }
interface MarkdownRenderer { validateLink: (value: string) => boolean; render: (value: string) => string; renderer?: { rules: Record<string, (tokens: MarkdownToken[], index: number) => string>; utils: { escapeHtml(value: string): string } } }
type MarkdownConstructor = new (options: { html: boolean; breaks: boolean; linkify: boolean }) => MarkdownRenderer;
function isMarkdownConstructor(value: unknown): value is MarkdownConstructor { return typeof value === "function"; }
function createMarkdownRenderer(MarkdownItCtor: MarkdownConstructor): MarkdownRenderer {
  const renderer = new MarkdownItCtor({ html: false, breaks: true, linkify: false });
  renderer.validateLink = isAllowedMarkdownLink;
  if (renderer.renderer !== undefined) {
    renderer.renderer.rules.image = (tokens, index) => {
      const token = tokens[index];
      const alt = renderer.renderer?.utils.escapeHtml(token?.content ?? "") ?? "";
      const src = renderer.renderer?.utils.escapeHtml(token?.attrGet("src") ?? "") ?? "";
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
  return fallbackMarkdown(source);
}

/** Markdown-it is configured with raw HTML disabled and its output is sanitized above. */
export function markdownToReact(body: unknown): React.ReactElement {
  return React.createElement("div", { className: "markdown-body", dangerouslySetInnerHTML: { __html: markdownToSafeHtml(body) } });
}
export const MarkdownBody = ({ body }: { body: string }): React.ReactElement => markdownToReact(body);
