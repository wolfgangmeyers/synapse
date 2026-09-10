import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState, type ReactElement } from "react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedDirectoryPicker } from "../../src/components/ManagedDirectoryPicker";

afterEach(cleanup);

describe("ManagedDirectoryPicker", () => {
  it("keeps browsing available beside an empty freeform folder input", async () => {
    const user = userEvent.setup();
    const list = vi.fn(async (path: string) => ({ path: path || "/allowed", parent: "", directories: [] }));
    render(<ManagedDirectoryPicker value="" onChange={() => undefined} list={list} />);

    const input = screen.getByLabelText("Working folder");
    const browse = screen.getByRole("button", { name: "Browse folders" });
    expect(input).toHaveAttribute("placeholder", "~/code/project");
    expect(browse).not.toBeDisabled();
    expect(input.nextElementSibling).toBe(browse);

    await user.click(browse);
    await waitFor(() => expect(list).toHaveBeenCalledWith(""));
  });

  it("shows at most the supplied local recent folders on focus and filters them as typed", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    function Picker(): ReactElement {
      const [value, setValue] = useState("");
      return <ManagedDirectoryPicker value={value} onChange={(next) => { onChange(next); setValue(next); }} list={async (path) => ({ path, parent: "", directories: [] })} recentFolders={["/projects/alpha", "/projects/beta"]} />;
    }
    render(<Picker />);

    const input = screen.getByLabelText("Working folder");
    await user.click(input);
    expect(screen.getByRole("list", { name: "Recent folders" })).toHaveTextContent("/projects/alpha");
    expect(screen.getByRole("list", { name: "Recent folders" })).toHaveTextContent("/projects/beta");

    await user.type(input, "beta");
    expect(screen.getByRole("list", { name: "Recent folders" })).toHaveTextContent("/projects/beta");
    expect(screen.queryByRole("button", { name: "/projects/alpha" })).not.toBeInTheDocument();
    expect(onChange).toHaveBeenCalledWith("beta");
  });

  it("uses a Finder file URI dropped on the picker as the freeform folder path", async () => {
    const onChange = vi.fn();
    const list = vi.fn(async (path: string) => ({ path, parent: "/allowed", directories: [] }));
    const { container } = render(<ManagedDirectoryPicker value="" onChange={onChange} list={list} />);
    const picker = container.querySelector(".managed-directory-picker");
    if (!(picker instanceof HTMLDivElement)) throw new Error("directory picker is missing");
    const transfer = { getData: (type: string) => type === "text/uri-list" ? "file:///allowed/dropped-folder\n" : "" };

    fireEvent.dragOver(picker, { dataTransfer: transfer });
    expect(picker).toHaveClass("dragging");
    fireEvent.drop(picker, { dataTransfer: transfer });

    await waitFor(() => expect(list).toHaveBeenCalledWith("/allowed/dropped-folder"));
    expect(onChange).toHaveBeenCalledWith("/allowed/dropped-folder");
    expect(picker).not.toHaveClass("dragging");
  });
});
