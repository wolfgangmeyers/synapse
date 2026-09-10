import { useId, useState, type DragEvent, type ReactElement } from "react";

export interface ManagedDirectoryPickerProps {
  value: string;
  recentFolders?: readonly string[];
  onChange(value: string): void;
  list(path: string): Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
}

function localFolderFromDrop(transfer: DataTransfer): string | undefined {
  const uriList = transfer.getData("text/uri-list").split("\n").map((value) => value.trim()).find((value) => value !== "" && !value.startsWith("#"));
  if (uriList !== undefined) {
    try {
      const url = new URL(uriList);
      if (url.protocol === "file:" && (url.host === "" || url.host === "localhost") && url.pathname !== "") return decodeURIComponent(url.pathname);
    } catch {
      // Fall through to a plain local path, which Finder supplies on some browsers.
    }
  }
  const plainPath = transfer.getData("text/plain").trim();
  return plainPath.startsWith("/") || plainPath === "~" || plainPath.startsWith("~/") ? plainPath : undefined;
}

// The typed path remains the accessible equivalent of the folder list. The
// router returns folders only and verifies containment before either value can
// become a launch working directory.
export function ManagedDirectoryPicker({ value, recentFolders = [], onChange, list }: ManagedDirectoryPickerProps): ReactElement {
  const inputID = useId();
  const [directories, setDirectories] = useState<Array<{ name: string; path: string }>>([]);
  const [parent, setParent] = useState("");
  const [error, setError] = useState("");
  const [recentOpen, setRecentOpen] = useState(false);
  const [dragging, setDragging] = useState(false);
  const query = value.trim().toLowerCase();
  const visibleRecentFolders = recentFolders.filter((folder) => folder.toLowerCase().includes(query));
  const browse = (path = value): void => {
    void list(path).then((result) => { onChange(result.path); setParent(result.parent); setDirectories(result.directories); setError(""); setRecentOpen(false); }).catch((cause: unknown) => setError(cause instanceof Error ? cause.message : "Folders could not be loaded."));
  };
  const dropFolder = (event: DragEvent<HTMLDivElement>): void => {
    event.preventDefault();
    setDragging(false);
    const folder = localFolderFromDrop(event.dataTransfer);
    if (folder === undefined) { setError("Drop a local folder from Finder."); return; }
    onChange(folder);
    browse(folder);
  };
  return <div className={`managed-directory-picker${dragging ? " dragging" : ""}`} onDragOver={(event) => { event.preventDefault(); setDragging(true); }} onDragLeave={() => setDragging(false)} onDrop={dropFolder}>
    <div className="managed-directory-field">
      <label htmlFor={inputID}>Working folder</label>
      <div className="directory-picker-input-row"><input id={inputID} required value={value} onFocus={() => setRecentOpen(true)} onChange={(event) => { onChange(event.currentTarget.value); setRecentOpen(true); }} placeholder="~/code/project" autoComplete="off" spellCheck={false} /><button type="button" className="secondary directory-browse-button" aria-label="Browse folders" title="Browse folders" onClick={() => browse()}><i className="fa-solid fa-folder-open" aria-hidden="true" /></button></div>
    </div>
    {recentOpen && recentFolders.length > 0 ? <ul className="managed-recent-folders" aria-label="Recent folders">{visibleRecentFolders.map((folder) => <li key={folder}><button type="button" className="secondary" onClick={() => browse(folder)}>{folder}</button></li>)}</ul> : null}
    <div className="directory-picker-actions">{parent ? <button type="button" className="secondary" onClick={() => browse(parent)}>Up one folder</button> : null}</div>
    {directories.length > 0 ? <ul aria-label="Folders">{directories.map((directory) => <li key={directory.path}><button type="button" className="secondary" onClick={() => browse(directory.path)}>{directory.name}</button></li>)}</ul> : null}
    <p className="managed-directory-drop-hint">Drop a folder from Finder.</p>
    {error ? <p className="error" role="alert">{error}</p> : null}
  </div>;
}
