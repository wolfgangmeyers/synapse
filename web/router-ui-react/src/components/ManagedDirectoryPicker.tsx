import { useEffect, useId, useRef, useState, type DragEvent, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";

interface ManagedDirectory {
  name: string;
  path: string;
}

interface DirectoryListing {
  path: string;
  parent: string;
  directories: ManagedDirectory[];
}

export interface ManagedDirectoryPickerProps {
  value: string;
  recentFolders?: readonly string[];
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
    const entry = item.kind === "file" && "webkitGetAsEntry" in item ? (item as DataTransferItem & { webkitGetAsEntry?: () => FileSystemEntry | null }).webkitGetAsEntry?.() : null;
    const path = pathFromText(entry?.isDirectory ? entry.fullPath : "");
    if (path !== undefined) return path;
  }
  return undefined;
}

// The launch form keeps its freeform input stable. Folder navigation belongs to
// the separate native dialog, and selection is committed only from that dialog.
export function ManagedDirectoryPicker({ value, recentFolders = [], onChange, list }: ManagedDirectoryPickerProps): ReactElement {
  const inputID = useId();
  const [recentOpen, setRecentOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [directories, setDirectories] = useState<ManagedDirectory[]>([]);
  const [parent, setParent] = useState("");
  const [currentPath, setCurrentPath] = useState("");
  const [selectedPath, setSelectedPath] = useState("");
  const [error, setError] = useState("");
  const requestID = useRef(0);
  const query = value.trim().toLowerCase();
  const visibleRecentFolders = recentFolders.filter((folder) => folder.toLowerCase().includes(query));

  const resetPicker = (): void => {
    requestID.current += 1;
    setPickerOpen(false);
    setDirectories([]);
    setParent("");
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
      if (folder === undefined) { setError("Drop a local folder path from Finder."); return; }
      onChange(folder);
      setRecentOpen(false);
      setError("");
    });
  };
  return <div className={`managed-directory-picker${dragging ? " dragging" : ""}`} onDragOver={(event) => { event.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)} onDrop={dropFolder}>
    <div className="managed-directory-field">
      <label htmlFor={inputID}>Working folder</label>
      <div className="directory-picker-input-row"><input id={inputID} required value={value} onFocus={() => setRecentOpen(true)} onChange={(event) => { onChange(event.currentTarget.value); setRecentOpen(true); }} placeholder="~/code/project" autoComplete="off" spellCheck={false} /><button type="button" className="secondary directory-browse-button" aria-label="Browse folders" title="Browse folders" onClick={openPicker}><i className="fa-solid fa-folder-open" aria-hidden="true" /></button></div>
    </div>
    {recentOpen && recentFolders.length > 0 ? <ul className="managed-recent-folders" aria-label="Recent folders">{visibleRecentFolders.map((folder) => <li key={folder}><button type="button" className="secondary" onClick={() => { onChange(folder); setRecentOpen(false); }}>{folder}</button></li>)}</ul> : null}
    <p className="managed-directory-drop-hint">Drop a folder from Finder.</p>
    {error ? <p className="error" role="alert">{error}</p> : null}
    <DialogShell open={pickerOpen} title="Choose working folder" titleId="managed-directory-browser-title" description="Choose a folder, then select it to use it." onClose={resetPicker} closeLabel="Close folder browser" className="message-dialog managed-directory-browser">
      <div className="managed-directory-browser-body">
        <p className="managed-directory-path" aria-live="polite">{currentPath || "Loading folders…"}</p>
        <div className="directory-picker-actions">{parent ? <button type="button" className="secondary" onClick={() => browse(parent)}>Up one folder</button> : null}</div>
        {directories.length > 0 ? <ul className="managed-directory-browser-list" aria-label="Folders">{directories.map((directory) => <li key={directory.path}><button type="button" className={selectedPath === directory.path ? "secondary selected" : "secondary"} onClick={() => { setSelectedPath(directory.path); browse(directory.path); }}>{directory.name}</button></li>)}</ul> : currentPath && !error ? <p>No folders here.</p> : null}
        {error ? <p className="error" role="alert">{error}</p> : null}
        <div className="dialog-actions"><button type="button" className="secondary" onClick={resetPicker}>Cancel</button><button type="button" disabled={!selectedPath} onClick={() => { onChange(selectedPath); resetPicker(); }}>Select folder</button></div>
      </div>
    </DialogShell>
  </div>;
}
