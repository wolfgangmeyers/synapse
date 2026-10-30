import { useEffect, useState, useMemo, useRef, type ReactElement } from "react";
import MarkdownIt from "markdown-it";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient } from "../transport/http-client";

export interface KnowledgeBaseDialogProps {
  open: boolean;
  client?: RouterHttpClient | undefined;
  onClose: () => void;
}

/**
 * Slugifies heading text for anchor IDs matching GitHub/markdown standard.
 */
export function slugifyHeading(text: string): string {
  return text
    .toLowerCase()
    .trim()
    .replace(/[^\w\s-]/g, "")
    .replace(/\s+/g, "-");
}

function extractPlainText(tokens: readonly { type: string; content?: string | undefined; children?: readonly unknown[] | undefined }[]): string {
  let text = "";
  for (const token of tokens) {
    if (token.children && Array.isArray(token.children)) {
      text += extractPlainText(token.children as readonly { type: string; content?: string | undefined; children?: readonly unknown[] | undefined }[]);
    } else if (token.type === "text" || token.type === "code_inline") {
      text += token.content ?? "";
    }
  }
  return text;
}

/**
 * Resolves a target link href relative to the currently active markdown file within the KB.
 * Handles hash fragments, relative paths ("..", "."), and returns a clean relative path.
 * If the href is purely a hash fragment or empty, returns null.
 */
export function resolveRelativePath(currentFile: string, targetHref: string): string | null {
  // Discard anchor fragments
  const [cleanHref] = targetHref.split("#");
  const trimmed = (cleanHref ?? "").trim();
  if (!trimmed) {
    return null;
  }

  // Extract current directory from currentFile
  const lastSlashIndex = currentFile.lastIndexOf("/");
  const currentDir = lastSlashIndex !== -1 ? currentFile.slice(0, lastSlashIndex) : "";

  // Combine segments
  const combined = currentDir ? `${currentDir}/${trimmed}` : trimmed;

  // Normalize segments (handling '..' and '.')
  const segments = combined.split("/");
  const normalizedSegments: string[] = [];

  for (const seg of segments) {
    if (!seg || seg === ".") {
      continue;
    }
    if (seg === "..") {
      normalizedSegments.pop();
    } else {
      normalizedSegments.push(seg);
    }
  }

  return normalizedSegments.join("/");
}

export function KnowledgeBaseDialog({
  open,
  client,
  onClose,
}: KnowledgeBaseDialogProps): ReactElement {
  const [githubUrl, setGithubUrl] = useState("");
  const [savedUrl, setSavedUrl] = useState("");
  const [currentFile, setCurrentFile] = useState("INDEX.md");
  const [content, setContent] = useState<string>("");
  const [targetHeading, setTargetHeading] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [status, setStatus] = useState<{ message: string; error?: boolean }>({ message: "" });
  const markdownBodyRef = useRef<HTMLDivElement>(null);

  // Load configured Git sync target on open
  useEffect(() => {
    if (!open) return;
    setCurrentFile("INDEX.md");
    setTargetHeading(null);
    setStatus({ message: "" });
    if (client) {
      void client
        .getGitSyncTarget("kb")
        .then((target) => {
          if (target?.repo_url) {
            setGithubUrl(target.repo_url);
            setSavedUrl(target.repo_url);
          } else {
            setGithubUrl("");
            setSavedUrl("");
          }
        })
        .catch(() => {});
    }
  }, [open, client]);

  // Load KB file whenever the selected path changes
  useEffect(() => {
    if (!open) return;
    if (client) {
      void client
        .getKBFile(currentFile)
        .then((text) => setContent(text))
        .catch(() => setContent(""));
    } else {
      const endpoint = `/v1/kb/file?path=${encodeURIComponent(currentFile)}`;
      void fetch(endpoint)
        .then((res) => (res.ok ? res.text() : ""))
        .then((text) => setContent(text))
        .catch(() => setContent(""));
    }
  }, [open, currentFile, client]);

  // Save Git sync repository URL
  const handleSaveGit = async () => {
    const trimmed = githubUrl.trim();
    if (!trimmed || saving) return;
    setSaving(true);
    setStatus({ message: "" });
    try {
      if (client) {
        const target = await client.setGitSyncTarget("kb", trimmed);
        setSavedUrl(target.repo_url);
        setStatus({ message: "Repository URL saved." });
      } else {
        const res = await fetch("/v1/git-sync/kb", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ repo_url: trimmed }),
        });
        if (res.ok) {
          setSavedUrl(trimmed);
          setStatus({ message: "Repository URL saved." });
        } else {
          setStatus({ message: "Failed to save repository URL.", error: true });
        }
      }
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Failed to save repository URL.", error: true });
    } finally {
      setSaving(false);
    }
  };

  // Clear Git sync repository URL
  const handleClearGit = async () => {
    if (saving || syncing) return;
    setSaving(true);
    setStatus({ message: "" });
    try {
      if (client) {
        await client.clearGitSyncTarget("kb");
      } else {
        await fetch("/v1/git-sync/kb", { method: "DELETE" });
      }
      setGithubUrl("");
      setSavedUrl("");
      setStatus({ message: "Repository URL cleared." });
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Failed to clear repository URL.", error: true });
    } finally {
      setSaving(false);
    }
  };

  // Trigger manual Git pull
  const handlePull = async () => {
    if (!client || syncing) return;
    setSyncing(true);
    setStatus({ message: "" });
    try {
      const res = await client.pullGitSync("kb");
      if (res.success === false) {
        setStatus({ message: res.error || "Git pull failed.", error: true });
      } else {
        setStatus({ message: "Successfully pulled changes from remote repository." });
        // Refresh currently viewed file
        const refreshed = await client.getKBFile(currentFile);
        setContent(refreshed);
      }
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Git pull failed.", error: true });
    } finally {
      setSyncing(false);
    }
  };

  // Trigger manual Git push
  const handlePush = async () => {
    if (!client || syncing) return;
    setSyncing(true);
    setStatus({ message: "" });
    try {
      const res = await client.pushGitSync("kb");
      if (res.success === false) {
        setStatus({ message: res.error || "Git push failed.", error: true });
      } else {
        setStatus({ message: "Successfully pushed changes to remote repository." });
      }
    } catch (err) {
      setStatus({ message: err instanceof Error ? err.message : "Git push failed.", error: true });
    } finally {
      setSyncing(false);
    }
  };

  // Configure Markdown parser:
  // - html: false disables raw HTML to prevent XSS / script injection.
  // - validateLink permits standard safe web protocols and relative markdown paths without schemes.
  // - heading_open rule injects id slugs for in-document anchor navigation (#hashtag links).
  const md = useMemo(() => {
    const parser = new MarkdownIt({ html: false, breaks: true, linkify: false });
    parser.validateLink = (url: string) => {
      const trimmed = url.trim();
      if (trimmed.startsWith("http://") || trimmed.startsWith("https://") || trimmed.startsWith("mailto:")) {
        return true;
      }
      // Allow relative paths (e.g. topic.md, sub/topic.md) and anchor links (#references) while disallowing dangerous schemes
      if (!trimmed.includes("://") && !trimmed.startsWith("//") && !trimmed.startsWith("javascript:")) {
        return true;
      }
      return false;
    };

    const defaultHeadingOpen =
      parser.renderer.rules.heading_open ||
      function (tokens, idx, options) {
        return parser.renderer.renderToken(tokens, idx, options);
      };

    parser.renderer.rules.heading_open = function (tokens, idx, options, env, self) {
      const nextToken = tokens[idx + 1];
      if (nextToken && nextToken.type === "inline") {
        const rawText = nextToken.children ? extractPlainText(nextToken.children) : (nextToken.content ?? "");
        const slug = slugifyHeading(rawText);
        if (slug) {
          tokens[idx]?.attrSet("id", slug);
        }
      }
      return defaultHeadingOpen(tokens, idx, options, env, self);
    };

    return parser;
  }, []);

  const renderedHtml = useMemo(() => {
    return md.render(content || "");
  }, [md, content]);

  // Reset or scroll to target heading when content is rendered or targetHeading changes.
  // Note: key={currentFile} on the container automatically resets scrollTop to 0 on file changes.
  useEffect(() => {
    const container = markdownBodyRef.current;
    if (!container) return;

    if (targetHeading) {
      const decodedTargetId = decodeURIComponent(targetHeading).toLowerCase();
      let el =
        container.querySelector(`#${CSS.escape(targetHeading)}`) ??
        container.querySelector(`#${CSS.escape(decodedTargetId)}`);

      if (!el) {
        const headings = container.querySelectorAll("h1, h2, h3, h4, h5, h6");
        for (const h of headings) {
          const id = h.getAttribute("id");
          if (id && id.toLowerCase() === decodedTargetId) {
            el = h;
            break;
          }
          if (slugifyHeading(h.textContent ?? "") === decodedTargetId) {
            el = h;
            break;
          }
        }
      }

      if (el instanceof HTMLElement) {
        el.scrollIntoView({ behavior: "smooth", block: "start" });
      }
    }
  }, [renderedHtml, targetHeading]);

  /**
   * Event delegation on the markdown container.
   * Intercepts clicks on:
   * 1. In-document anchor links (#hashtag) -> scrolls to matching heading element.
   * 2. Relative markdown links (e.g. href="topic.md") -> updates currentFile SPA navigation.
   */
  const handleLinkClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const target = e.target instanceof HTMLElement ? e.target : null;
    const anchor = target?.closest("a");
    if (!anchor) return;

    const href = anchor.getAttribute("href");
    if (!href || href.startsWith("http://") || href.startsWith("https://") || href.startsWith("mailto:")) {
      return;
    }

    // Relative link to current file or file with hash (e.g. "INDEX.md#references" or "file.md#target" or "#target")
    const hashIndex = href.indexOf("#");
    const rawPath = hashIndex !== -1 ? href.slice(0, hashIndex) : href;
    const rawTargetId = hashIndex !== -1 ? href.slice(hashIndex + 1).trim() : "";

    // If it's a hash pointing within the current file (e.g. "#target" or "currentFile#target")
    if (href.startsWith("#") || (rawTargetId && (rawPath === "" || rawPath === currentFile || resolveRelativePath(currentFile, rawPath) === currentFile))) {
      e.preventDefault();
      if (!rawTargetId) return;

      const container = e.currentTarget;
      const decodedTargetId = decodeURIComponent(rawTargetId).toLowerCase();

      // Attempt matching by exact ID, decoded lowercase ID, or heading text matching
      let el =
        container.querySelector(`#${CSS.escape(rawTargetId)}`) ??
        container.querySelector(`#${CSS.escape(decodedTargetId)}`);

      if (!el) {
        // Fallback: search headings for matching id or slugified text
        const headings = container.querySelectorAll("h1, h2, h3, h4, h5, h6");
        for (const h of headings) {
          const id = h.getAttribute("id");
          if (id && id.toLowerCase() === decodedTargetId) {
            el = h;
            break;
          }
          if (slugifyHeading(h.textContent ?? "") === decodedTargetId) {
            el = h;
            break;
          }
        }
      }

      if (el instanceof HTMLElement) {
        el.scrollIntoView({ behavior: "smooth", block: "start" });
      }
      return;
    }

    // Relative links (potentially to another file with anchor fragment, e.g. "operations/worker.md#details")
    e.preventDefault();
    const resolved = resolveRelativePath(currentFile, href);
    if (resolved) {
      setTargetHeading(rawTargetId || null);
      setCurrentFile(resolved);
    }
  };

  return (
    <DialogShell
      open={open}
      title="Knowledge Base"
      titleId="knowledge-base-dialog-title"
      description="Browse persistent markdown documents and configure GitHub sync."
      onClose={onClose}
      className="message-dialog kb-dialog"
    >
      <div className="kb-dialog-body">
        <div className="kb-github-sync">
          <input
            type="text"
            className="kb-github-url-input"
            placeholder="GitHub repository URL"
            value={githubUrl}
            onChange={(e) => setGithubUrl(e.target.value)}
            disabled={saving || syncing}
            style={{ flex: 1 }}
          />
          <button
            type="button"
            className="kb-github-save-btn"
            disabled={saving || syncing || githubUrl.trim() === savedUrl || !githubUrl.trim()}
            onClick={() => void handleSaveGit()}
          >
            {saving ? "Saving…" : "Save"}
          </button>
          {savedUrl ? (
            <>
              <button
                type="button"
                className="icon-btn-round kb-github-pull-btn"
                title="Pull from GitHub"
                aria-label="Pull from GitHub"
                disabled={saving || syncing}
                onClick={() => void handlePull()}
              >
                <i className={`fa-solid ${syncing ? "fa-spinner fa-spin" : "fa-arrow-down"}`} aria-hidden="true" />
              </button>
              <button
                type="button"
                className="icon-btn-round kb-github-push-btn"
                title="Push to GitHub"
                aria-label="Push to GitHub"
                disabled={saving || syncing}
                onClick={() => void handlePush()}
              >
                <i className={`fa-solid ${syncing ? "fa-spinner fa-spin" : "fa-arrow-up"}`} aria-hidden="true" />
              </button>
              <button
                type="button"
                className="icon-btn-round kb-github-clear-btn"
                title="Clear repository URL"
                aria-label="Clear repository URL"
                disabled={saving || syncing}
                onClick={() => void handleClearGit()}
              >
                <i className="fa-solid fa-trash" aria-hidden="true" />
              </button>
            </>
          ) : null}
        </div>

        {status.message ? (
          <p
            className={`kb-dialog-status ${status.error ? "error" : ""}`}
            style={{ margin: 0, fontSize: "0.85rem", color: status.error ? "var(--danger, #ff5555)" : "var(--success, #55ff55)" }}
          >
            {status.message}
          </p>
        ) : null}

        <div className="kb-browser">
          {currentFile !== "INDEX.md" ? (
            <div className="kb-nav-header">
              <button
                type="button"
                className="icon-btn-round kb-back-btn"
                onClick={() => {
                  setTargetHeading(null);
                  setCurrentFile("INDEX.md");
                }}
                title="Back to Index"
                aria-label="Back to Index"
              >
                <i className="fa-solid fa-arrow-left" aria-hidden="true" />
              </button>
              <span className="kb-current-path" style={{ fontSize: "0.85rem", opacity: 0.8, fontFamily: "monospace" }}>
                {currentFile}
              </span>
            </div>
          ) : null}
          <div
            key={currentFile}
            ref={markdownBodyRef}
            className="markdown-body"
            onClick={handleLinkClick}
            dangerouslySetInnerHTML={{ __html: renderedHtml }}
          />
        </div>
      </div>
    </DialogShell>
  );
}
