import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState, type ReactElement } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedDirectoryPicker } from "../../src/components/ManagedDirectoryPicker";

afterEach(cleanup);

function StatefulPicker({ list = async (path: string) => ({ path: path || "/allowed", parent: "", directories: [] }) }: { list?: (path: string) => Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }> }): ReactElement {
  const [value, setValue] = useState("");
  return <ManagedDirectoryPicker value={value} onChange={setValue} list={list} recentFolders={["/projects/alpha", "/projects/beta"]} />;
}

describe("ManagedDirectoryPicker", () => {
  it("opens a separate native folder dialog without changing the freeform input", async () => {
    const user = userEvent.setup();
    const list = vi.fn(async (path: string) => ({ path: path || "/allowed", parent: "", directories: [{ name: "project", path: "/allowed/project" }] }));
    render(<StatefulPicker list={list} />);

    const input = screen.getByLabelText("Working folder");
    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    const dialog = await screen.findByRole("dialog", { name: "Choose working folder" });
    expect(input).toHaveValue("");
    expect(list).toHaveBeenCalledWith("");
    expect(within(dialog).getByRole("button", { name: "project" })).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Folders" })).toBeInTheDocument();
    expect(document.querySelector(".managed-directory-picker > [aria-label='Folders']")).not.toBeInTheDocument();
  });

  it("navigates by breadcrumb or Up and commits only with Select folder", async () => {
    const user = userEvent.setup();
    const list = vi.fn(async (path: string) => path === "/Users/wolfgang/code/project" ? ({ path, parent: "/Users/wolfgang/code", breadcrumbs: [{ name: "wolfgang", path: "/Users/wolfgang" }, { name: "code", path: "/Users/wolfgang/code" }, { name: "project", path }], directories: [{ name: "nested", path: "/Users/wolfgang/code/project/nested" }] }) : path === "/Users/wolfgang/code" ? ({ path, parent: "/Users/wolfgang", breadcrumbs: [{ name: "wolfgang", path: "/Users/wolfgang" }, { name: "code", path }], directories: [{ name: "project", path: "/Users/wolfgang/code/project" }] }) : ({ path: path || "/Users/wolfgang", parent: "", breadcrumbs: [{ name: "wolfgang", path: path || "/Users/wolfgang" }], directories: [{ name: "code", path: "/Users/wolfgang/code" }] }));
    render(<StatefulPicker list={list} />);

    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    const dialog = await screen.findByRole("dialog", { name: "Choose working folder" });
    await user.click(within(dialog).getByRole("button", { name: "code" }));
    await user.click(within(dialog).getByRole("button", { name: "project" }));
    await waitFor(() => expect(list).toHaveBeenCalledWith("/Users/wolfgang/code/project"));
    expect(screen.getByLabelText("Working folder")).toHaveValue("");
    expect(within(dialog).getByRole("navigation", { name: "Folder path" })).toHaveTextContent("wolfgang");
    await user.click(within(dialog).getByRole("button", { name: "code" }));
    await waitFor(() => expect(list).toHaveBeenCalledWith("/Users/wolfgang/code"));
    await user.click(within(dialog).getByRole("button", { name: "Up one folder" }));
    await waitFor(() => expect(list).toHaveBeenCalledWith("/Users/wolfgang"));
    await user.click(within(dialog).getByRole("button", { name: "code" }));
    await user.click(within(dialog).getByRole("button", { name: "project" }));
    await user.click(within(dialog).getByRole("button", { name: "Select folder" }));
    expect(screen.queryByRole("dialog", { name: "Choose working folder" })).not.toBeInTheDocument();
    expect(screen.getByLabelText("Working folder")).toHaveValue("/Users/wolfgang/code/project");
  });

  it("clears picker state on cancel and ignores an old listing when reopened", async () => {
    const user = userEvent.setup();
    let firstResolve: ((value: { path: string; parent: string; directories: Array<{ name: string; path: string }> }) => void) | undefined;
    const list = vi.fn((path: string) => new Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>((resolve) => { if (!firstResolve) firstResolve = resolve; else resolve({ path: path || "/fresh", parent: "", directories: [{ name: "fresh", path: "/fresh/folder" }] }); }));
    render(<StatefulPicker list={list} />);

    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog", { name: "Choose working folder" })).not.toBeInTheDocument();
    firstResolve?.({ path: "/stale", parent: "", directories: [{ name: "stale", path: "/stale/folder" }] });
    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    const dialog = await screen.findByRole("dialog", { name: "Choose working folder" });
    expect(within(dialog).queryByRole("button", { name: "stale" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "fresh" })).toBeInTheDocument();
  });

  it("keeps recent suggestions beside the freeform input", async () => {
    const user = userEvent.setup();
    render(<StatefulPicker />);
    const input = screen.getByLabelText("Working folder");
    await user.click(input);
    expect(screen.getByRole("list", { name: "Recent folders" })).toHaveTextContent("/projects/alpha");
    await user.type(input, "beta");
    expect(screen.queryByRole("button", { name: "/projects/alpha" })).not.toBeInTheDocument();
  });

  it("accepts a Finder Files payload without opening a folder listing", async () => {
    const onChange = vi.fn();
    const list = vi.fn(async (path: string) => ({ path, parent: "", directories: [] }));
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={list} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    const transfer = { getData: () => "", files: [{ path: "/allowed/dropped-folder" }], items: [] };

    fireEvent.dragOver(picker, { dataTransfer: transfer });
    fireEvent.drop(picker, { dataTransfer: transfer });

    await waitFor(() => expect(onChange).toHaveBeenCalledWith("/allowed/dropped-folder"));
    expect(list).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog", { name: "Choose working folder" })).not.toBeInTheDocument();
  });

  it("keeps Finder's absolute file URL when the entry fallback only has a root-relative name", async () => {
    const onChange = vi.fn();
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={async (path) => ({ path, parent: "", directories: [] })} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    fireEvent.drop(picker, { dataTransfer: { getData: (type: string) => type === "public.file-url" ? "file:///Users/wolfgang/code/linear-mcp-cli\r\n" : "", files: [], items: [{ kind: "file", webkitGetAsEntry: () => ({ isDirectory: true, fullPath: "/linear-mcp-cli" }) }] } });
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("/Users/wolfgang/code/linear-mcp-cli"));
  });

  it("does not turn Finder's root-relative entry name into a launch path", async () => {
    const onChange = vi.fn();
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={async (path) => ({ path, parent: "", directories: [] })} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    fireEvent.drop(picker, { dataTransfer: { getData: () => "", files: [], items: [{ kind: "file", webkitGetAsEntry: () => ({ isDirectory: true, fullPath: "/linear-mcp-cli" }) }] } });
    expect(await screen.findByRole("alert")).toHaveTextContent("Finder did not provide the local folder path. Use Browse or paste it.");
    expect(onChange).not.toHaveBeenCalled();
  });

  it("accepts a Finder item string when getData is unavailable", async () => {
    const onChange = vi.fn();
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={async (path) => ({ path, parent: "", directories: [] })} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    fireEvent.drop(picker, { dataTransfer: { getData: () => "", files: [], items: [{ kind: "string", getAsString: (callback: (value: string) => void) => callback("file:///allowed/item-folder") }] } });
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("/allowed/item-folder"));
  });

  it("accepts an absolute path exposed by Finder's File payload", async () => {
    const onChange = vi.fn();
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={async (path) => ({ path, parent: "", directories: [] })} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    fireEvent.drop(picker, { dataTransfer: { getData: () => "", files: [{ path: "/Users/wolfgang/code/linear-mcp-cli" }], items: [] } });
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("/Users/wolfgang/code/linear-mcp-cli"));
  });

  it("keeps Finder's absolute DataTransferItem File path over its root-relative entry path", async () => {
    const onChange = vi.fn();
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={async (path) => ({ path, parent: "", directories: [] })} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    fireEvent.drop(picker, { dataTransfer: { getData: () => "", files: [], items: [{ kind: "file", getAsFile: () => ({ path: "/Users/wolfgang/code/linear-mcp-cli" }), webkitGetAsEntry: () => ({ isDirectory: true, fullPath: "/linear-mcp-cli" }) }] } });
    await waitFor(() => expect(onChange).toHaveBeenCalledWith("/Users/wolfgang/code/linear-mcp-cli"));
  });

  it("reports a clear error for a drop without a local folder path", async () => {
    const { container } = render(<ManagedDirectoryPicker value="" onChange={() => undefined} list={async (path) => ({ path, parent: "", directories: [] })} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    fireEvent.drop(picker, { dataTransfer: { getData: () => "", files: [], items: [] } });
    expect(await screen.findByRole("alert")).toHaveTextContent("Finder did not provide the local folder path. Use Browse or paste it.");
  });

  it("provides a datalist with recent folders connected to the input", () => {
    render(<StatefulPicker />);
    const input = screen.getByLabelText("Working folder");
    const datalistId = input.getAttribute("list");
    expect(datalistId).toBeTruthy();
    const datalist = document.getElementById(datalistId!);
    expect(datalist).toBeInTheDocument();
    expect(datalist?.tagName.toLowerCase()).toBe("datalist");
    const options = Array.from(datalist?.querySelectorAll("option") ?? []).map((opt) => opt.value);
    expect(options).toEqual(["/projects/alpha", "/projects/beta"]);
  });
});
