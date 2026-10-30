import { useEffect, useState, useMemo, type ReactElement } from "react";
import MarkdownIt from "markdown-it";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient } from "../transport/http-client";

export interface KnowledgeBaseDialogProps {
  open: boolean;
  client?: RouterHttpClient | undefined;
  onClose: () => void;
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
  const [saving, setSaving] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [status, setStatus] = useState<{ message: string; error?: boolean }>({ message: "" });

  // Load configured Git sync target on open
  useEffect(() => {
    if (!open) return;
    setCurrentFile("INDEX.md");
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
  const md = useMemo(() => {
    const parser = new MarkdownIt({ html: false, breaks: true, linkify: false });
    parser.validateLink = (url: string) => {
      const trimmed = url.trim();
      if (trimmed.startsWith("http://") || trimmed.startsWith("https://") || trimmed.startsWith("mailto:")) {
        return true;
      }
      // Allow relative paths (e.g. topic.md, sub/topic.md) while disallowing dangerous schemes
      if (!trimmed.includes("://") && !trimmed.startsWith("//") && !trimmed.startsWith("javascript:")) {
        return true;
      }
      return false;
    };
    return parser;
  }, []);

  const renderedHtml = useMemo(() => {
    return md.render(content || "");
  }, [md, content]);

  /**
   * Event delegation on the markdown container.
   * Intercepts clicks on relative markdown links (e.g. href="topic.md") to keep
   * browsing within the dialog SPA without triggering full page reloads.
   */
  const handleLinkClick = (e: React.MouseEvent<HTMLDivElement>) => {
    const target = e.target instanceof HTMLElement ? e.target : null;
    const anchor = target?.closest("a");
    if (anchor) {
      const href = anchor.getAttribute("href");
      if (href && !href.startsWith("http://") && !href.startsWith("https://") && !href.startsWith("mailto:")) {
        if (href.startsWith("#")) {
          return;
        }
        e.preventDefault();
        const resolved = resolveRelativePath(currentFile, href);
        if (resolved) {
          setCurrentFile(resolved);
        }
      }
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
                onClick={() => setCurrentFile("INDEX.md")}
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
            className="markdown-body"
            onClick={handleLinkClick}
            dangerouslySetInnerHTML={{ __html: renderedHtml }}
          />
        </div>
      </div>
    </DialogShell>
  );
}
