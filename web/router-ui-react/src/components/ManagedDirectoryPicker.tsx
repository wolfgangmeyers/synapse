import { useState, type ReactElement } from "react";

export interface ManagedDirectoryPickerProps {
  value: string;
  onChange(value: string): void;
  list(path: string): Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
}

// The typed path remains the accessible equivalent of the folder list. The
// router returns folders only and verifies containment before either value can
// become a launch working directory.
export function ManagedDirectoryPicker({ value, onChange, list }: ManagedDirectoryPickerProps): ReactElement {
  const [directories, setDirectories] = useState<Array<{ name: string; path: string }>>([]);
  const [parent, setParent] = useState("");
  const [error, setError] = useState("");
  const browse = (path = value): void => {
    void list(path).then((result) => { onChange(result.path); setParent(result.parent); setDirectories(result.directories); setError(""); }).catch((cause: unknown) => setError(cause instanceof Error ? cause.message : "Folders could not be loaded."));
  };
  return <div className="managed-directory-picker"><label>Working folder<input required value={value} onChange={(event) => onChange(event.currentTarget.value)} placeholder="/Users/me/project" autoComplete="off" /></label><div className="directory-picker-actions"><button type="button" className="secondary" disabled={!value} onClick={() => browse()}>Browse folders</button>{parent ? <button type="button" className="secondary" onClick={() => browse(parent)}>Up one folder</button> : null}</div>{directories.length > 0 ? <ul aria-label="Folders">{directories.map((directory) => <li key={directory.path}><button type="button" className="secondary" onClick={() => browse(directory.path)}>{directory.name}</button></li>)}</ul> : null}{error ? <p className="error" role="alert">{error}</p> : null}</div>;
}
