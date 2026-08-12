let defaultRenderer;

const ALLOWED_LINK_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

export function isAllowedMarkdownLink(value) {
  if (typeof value !== "string" || value.trim() !== value || value.length === 0) return false;
  try {
    const url = new URL(value);
    if (!ALLOWED_LINK_PROTOCOLS.has(url.protocol)) return false;
    if (url.protocol === "mailto:") return /^mailto:[^\s/]+$/i.test(value);
    return value.startsWith(`${url.protocol}//`);
  } catch {
    return false;
  }
}

export function textWithoutMarkdownImages(body) {
  const imageReferenceLabels = new Set();
  const withoutInlineImages = String(body ?? "").replace(/!\[[^\]\n]*\]\([^\)\n]*(?:\s+"[^"\n]*")?\)/g, "");
  const withoutFullReferenceImages = withoutInlineImages.replace(/!\[([^\]\n]*)\]\[([^\]\n]*)\]/g, (_match, alt, label) => {
    imageReferenceLabels.add(normalizeMarkdownReferenceLabel(label || alt));
    return "";
  });
  const withoutReferenceImages = withoutFullReferenceImages.replace(/!\[([^\]\n]+)\](?![\[(])/g, (_match, alt) => {
    imageReferenceLabels.add(normalizeMarkdownReferenceLabel(alt));
    return "";
  });
  if (imageReferenceLabels.size === 0) return withoutReferenceImages;
  return withoutReferenceImages
    .split("\n")
    .filter((line) => !imageReferenceLabels.has(markdownReferenceDefinitionLabel(line)))
    .join("\n");
}

function normalizeMarkdownReferenceLabel(label) {
  return String(label || "").trim().replace(/\s+/g, " ").toLowerCase();
}

function markdownReferenceDefinitionLabel(line) {
  const match = /^ {0,3}\[([^\]]+)\]:/.exec(line);
  return match ? normalizeMarkdownReferenceLabel(match[1]) : "";
}

export function createMarkdownRenderer(MarkdownIt = globalThis.markdownit) {
  if (typeof MarkdownIt !== "function") throw new Error("Markdown renderer is not loaded");
  const renderer = new MarkdownIt({ html: false, breaks: true, linkify: false });
  renderer.validateLink = isAllowedMarkdownLink;
  renderer.renderer.rules.image = (tokens, index) => {
    const token = tokens[index];
    const alt = renderer.utils.escapeHtml(token.content || "");
    const src = renderer.utils.escapeHtml(token.attrGet("src") || "");
    return `![${alt}](${src})`;
  };
  return renderer;
}

function markdownRenderer() {
  defaultRenderer ??= createMarkdownRenderer();
  return defaultRenderer;
}

export function markdownToSafeHtml(body, MarkdownIt) {
  const renderer = MarkdownIt ? createMarkdownRenderer(MarkdownIt) : markdownRenderer();
  return renderer.render(String(body ?? ""));
}

export function renderMarkdownBody(document, body) {
  const element = document.createElement("div");
  element.className = "markdown-body";
  element.innerHTML = markdownToSafeHtml(body);
  for (const link of element.querySelectorAll("a[href]")) {
    link.target = "_blank";
    link.rel = "noopener noreferrer";
  }
  return element;
}
