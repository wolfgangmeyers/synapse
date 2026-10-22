import { useEffect, useId, useRef, useState, type DragEvent, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";

interface ManagedDirectory {
  name: string;
  path: string;
}

interface DirectoryListing {
  path: string;
  parent: string;
  breadcrumbs?: ManagedDirectory[];
  directories: ManagedDirectory[];
}

export interface ManagedDirectoryPickerProps {
  value: string;
  recentFolders?: readonly string[];
  isCheckingGit?: boolean;
  onChange(value: string): void;
  list(path: string): Promise<DirectoryListing>;
}

function pathFromText(value: string): string | undefined {
  const candidate = value.trim().replace(/^['"]|['"]$/g, "");
  if (candidate === "") return undefined;
  try {
    const url = new URL(candidate);
    if (url.protocol === "file:" && (url.host === "" || url.host === "localhost") && url.pathname !== "") return decodeURIComponent(url.pathname);
  } catch {
    // Finder and browsers do not agree on whether they expose a file URL or a path.
  }
  return candidate.startsWith("/") || candidate === "~" || candidate.startsWith("~/") ? candidate : undefined;
}

async function localFolderFromDrop(transfer: DataTransfer): Promise<string | undefined> {
  for (const type of ["text/uri-list", "text/plain", "text/x-moz-url", "public.file-url"]) {
    const path = pathFromText(transfer.getData(type).split("\n").find((value) => value.trim() !== "" && !value.trim().startsWith("#")) ?? "");
    if (path !== undefined) return path;
  }
  for (const file of Array.from(transfer.files)) {
    const droppedFile = file as File & { path?: unknown };
    const path = pathFromText(typeof droppedFile.path === "string" ? droppedFile.path : "");
    if (path !== undefined) return path;
  }
  for (const item of Array.from(transfer.items)) {
    if (item.kind !== "string") continue;
    const text = await new Promise<string>((resolve) => item.getAsString((value) => resolve(value)));
    const path = pathFromText(text.split("\n").find((value) => value.trim() !== "" && !value.trim().startsWith("#")) ?? "");
    if (path !== undefined) return path;
  }
  for (const item of Array.from(transfer.items)) {
    if (item.kind !== "file" || typeof item.getAsFile !== "function") continue;
    const droppedFile = item.getAsFile() as (File & { path?: unknown }) | null;
    const path = pathFromText(typeof droppedFile?.path === "string" ? droppedFile.path : "");
    if (path !== undefined) return path;
  }
  // Chromium's FileSystemEntry.fullPath is relative to the dropped-item
  // virtual root (for example, `/linear-mcp-cli`), not a local filesystem
  // path. It cannot safely become a launch directory.
  return undefined;
}

// The launch form keeps its freeform input stable. Folder navigation belongs to
// the separate native dialog, and selection is committed only from that dialog.
export function ManagedDirectoryPicker({ value, recentFolders = [], isCheckingGit = false, onChange, list }: ManagedDirectoryPickerProps): ReactElement {
  const inputID = useId();
  const searchID = useId();
  const menuRef = useRef<HTMLDivElement | null>(null);
  const searchRef = useRef<HTMLInputElement | null>(null);
  const [recentOpen, setRecentOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [dragging, setDragging] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [directories, setDirectories] = useState<ManagedDirectory[]>([]);
  const [parent, setParent] = useState("");
  const [breadcrumbs, setBreadcrumbs] = useState<ManagedDirectory[]>([]);
  const [currentPath, setCurrentPath] = useState("");
  const [selectedPath, setSelectedPath] = useState("");
  const [error, setError] = useState("");
  const requestID = useRef(0);

  const topRecentFolders = recentFolders.slice(0, 15);
  const query = searchQuery.trim().toLowerCase();
  const visibleRecentFolders = query === ""
    ? topRecentFolders
    : topRecentFolders.filter((folder) => folder.toLowerCase().includes(query));

  const toggleRecent = (): void => {
    setRecentOpen((open) => {
      const next = !open;
      if (next) {
        setSearchQuery("");
        requestAnimationFrame(() => {
          searchRef.current?.focus();
        });
      }
      return next;
    });
  };

  const closeRecent = (): void => {
    setRecentOpen(false);
  };

  useEffect(() => {
    if (!recentOpen) return;
    const handlePointerDown = (event: PointerEvent): void => {
      const target = event.target;
      if (target instanceof Node && !menuRef.current?.contains(target)) {
        closeRecent();
      }
    };
    const handleKeyDown = (event: KeyboardEvent): void => {
      if (event.key === "Escape") {
        event.stopPropagation();
        closeRecent();
      }
    };
    window.addEventListener("pointerdown", handlePointerDown);
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("pointerdown", handlePointerDown);
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [recentOpen]);

  const resetPicker = (): void => {
    requestID.current += 1;
    setPickerOpen(false);
    setDirectories([]);
    setParent("");
    setBreadcrumbs([]);
    setCurrentPath("");
    setSelectedPath("");
    setError("");
  };
  const browse = (path: string): void => {
    const id = ++requestID.current;
    setError("");
    void list(path).then((result) => {
      if (id !== requestID.current) return;
      setCurrentPath(result.path);
      setParent(result.parent);
      setBreadcrumbs(result.breadcrumbs ?? []);
      setDirectories(result.directories);
      setSelectedPath(result.path);
    }).catch((cause: unknown) => {
      if (id === requestID.current) setError(cause instanceof Error ? cause.message : "Folders could not be loaded.");
    });
  };
  const openPicker = (): void => {
    setPickerOpen(true);
    setRecentOpen(false);
    setDirectories([]);
    setParent("");
    setBreadcrumbs([]);
    setCurrentPath("");
    setSelectedPath("");
    setError("");
    browse(value.trim());
  };
  useEffect(() => () => { requestID.current += 1; }, []);
  const dropFolder = (event: DragEvent<HTMLDivElement>): void => {
    event.preventDefault();
    setDragging(false);
    void localFolderFromDrop(event.dataTransfer).then((folder) => {
      if (folder === undefined) { setError("Finder did not provide the local folder path. Use Browse or paste it."); return; }
      onChange(folder);
      setRecentOpen(false);
      setError("");
    });
  };
  return <div className={`managed-directory-picker${dragging ? " dragging" : ""}`} onDragOver={(event) => { event.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)} onDrop={dropFolder}>
    <div className="managed-directory-field">
      <label htmlFor={inputID}>Working folder</label>
      <div className="directory-picker-input-row">
        <div className="directory-picker-input-group" ref={menuRef}>
          <input
            id={inputID}
            required
            value={value}
            onChange={(event) => onChange(event.currentTarget.value)}
            placeholder="~/code/project"
            autoComplete="off"
            spellCheck={false}
          />
          <button
            type="button"
            className="directory-picker-toggle-button"
            aria-label="Recent folders"
            aria-expanded={recentOpen}
            aria-haspopup="dialog"
            onClick={toggleRecent}
            title="Recent folders"
          >
            <i className={`fa-solid ${recentOpen ? "fa-chevron-up" : "fa-chevron-down"}`} aria-hidden="true" />
          </button>
          {recentOpen ? (
            <div className="model-menu" role="dialog" aria-label="Choose working folder">
              <input
                ref={searchRef}
                id={searchID}
                className="model-search"
                type="search"
                aria-label="Search folders"
                placeholder="Search folders…"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
              />
              <div className="model-options" role="listbox">
                {visibleRecentFolders.length === 0 ? (
                  <p className="model-options-empty">No matching folders</p>
                ) : (
                  visibleRecentFolders.map((folder) => (
                    <button
                      key={folder}
                      type="button"
                      className="model-option"
                      role="option"
                      aria-label={`Select folder ${folder}`}
                      onClick={() => { onChange(folder); setRecentOpen(false); }}
                    >
                      <strong>{folder}</strong>
                    </button>
                  ))
                )}
              </div>
            </div>
          ) : null}
        </div>
        <button type="button" className="secondary directory-browse-button" aria-label="Browse folders" title="Browse folders" onClick={openPicker}>
          <i className="fa-solid fa-folder-open" aria-hidden="true" />
        </button>
      </div>
    </div>
    {isCheckingGit ? (
      <p className="managed-directory-drop-hint managed-directory-git-checking">
        Checking git… <i className="fa-solid fa-spinner fa-spin" aria-hidden="true" />
      </p>
    ) : (
      <p className="managed-directory-drop-hint">Drop a folder from Finder.</p>
    )}
    {error ? <p className="error" role="alert">{error}</p> : null}
    <DialogShell open={pickerOpen} title="Choose working folder" titleId="managed-directory-browser-title" description="Choose a folder, then select it to use it." onClose={resetPicker} closeLabel="Close folder browser" className="message-dialog managed-directory-browser">
      <div className="managed-directory-browser-body">
        <nav className="managed-directory-breadcrumbs" aria-label="Folder path">{breadcrumbs.map((breadcrumb, index) => <span key={breadcrumb.path}>{index > 0 ? <span className="managed-directory-breadcrumb-separator" aria-hidden="true">/</span> : null}<button type="button" className="secondary" disabled={breadcrumb.path === currentPath} aria-current={breadcrumb.path === currentPath ? "page" : undefined} onClick={() => browse(breadcrumb.path)}>{breadcrumb.name}</button></span>)}</nav>
        <p className="managed-directory-path" aria-live="polite">{currentPath || "Loading folders…"}</p>
        <div className="directory-picker-actions">{parent ? <button type="button" className="secondary" onClick={() => browse(parent)}>Up one folder</button> : null}</div>
        {directories.length > 0 ? <ul className="managed-directory-browser-list" aria-label="Folders">{directories.map((directory) => <li key={directory.path}><button type="button" className={selectedPath === directory.path ? "secondary selected" : "secondary"} onClick={() => { setSelectedPath(directory.path); browse(directory.path); }}>{directory.name}</button></li>)}</ul> : currentPath && !error ? <p>No folders here.</p> : null}
        {error ? <p className="error" role="alert">{error}</p> : null}
        <div className="dialog-actions"><button type="button" className="secondary" onClick={resetPicker}>Cancel</button><button type="button" disabled={!selectedPath} onClick={() => { onChange(selectedPath); resetPicker(); }}>Select folder</button></div>
      </div>
    </DialogShell>
  </div>;
}
