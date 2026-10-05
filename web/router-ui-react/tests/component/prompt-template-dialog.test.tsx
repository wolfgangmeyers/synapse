import { describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { PromptTemplateDialog } from "../../src/components/PromptTemplateDialog";
import type { PromptTemplate } from "../../src/domain/prompt-templates";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const template: PromptTemplate = { id: "t1", name: "Review", body: "Review ${file}" };
const base = { open: true, templates: [template], onClose: vi.fn(), onSave: vi.fn(), onDelete: vi.fn(), onImport: vi.fn() };

describe("PromptTemplateDialog", () => {
  it("exports through a browser download even when no callback is supplied", async () => {
    const user = userEvent.setup();
    let exportedBlob: Blob | undefined;
    const createObjectURL = vi.fn((value: Blob) => { exportedBlob = value; return "blob:prompt-templates"; });
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      expect(document.body.contains(this)).toBe(true);
      expect(this.hidden).toBe(false);
      expect(this.download).toBe("prompt-templates.json");
      expect(this.href).toBe("blob:prompt-templates");
    });
    render(<PromptTemplateDialog {...base} />);
    await user.click(screen.getByTestId("prompt-export"));
    expect(createObjectURL).toHaveBeenCalledOnce();
    expect(click).toHaveBeenCalledOnce();
    await vi.waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:prompt-templates"));
    expect(exportedBlob).toBeInstanceOf(Blob);
  });

  it("keeps exporting when jsdom does not provide URL.revokeObjectURL", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("URL", { createObjectURL: vi.fn(() => "blob:prompt-templates") });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(<PromptTemplateDialog {...base} />);
    await user.click(screen.getByTestId("prompt-export"));
    expect(await screen.findByRole("status")).toHaveTextContent("Templates exported.");
  });

  it("imports immediately and overwrites existing templates with matching names without a conflict confirmation prompt", async () => {
    const user = userEvent.setup();
    const onExport = vi.fn();
    const onImport = vi.fn().mockResolvedValue({ imported: 1, skipped: 0 });
    vi.stubGlobal("URL", { createObjectURL: vi.fn(() => "blob:prompt-templates"), revokeObjectURL: vi.fn() });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    render(<PromptTemplateDialog {...base} onExport={onExport} onImport={onImport} />);

    const input = document.querySelector("input[type='file']");
    expect(input).not.toBeNull();
    if (!(input instanceof HTMLInputElement)) throw new Error("Import input is missing");
    const file = new File([JSON.stringify({ format: "mecha-prompt-templates", version: 1, templates: [{ name: "Review", body: "New body" }] })], "templates.json", { type: "application/json" });
    fireEvent.change(input, { target: { files: [file] } });

    // Directly invokes onImport with overwrite=true and no "Import and overwrite" conflict dialog
    expect(screen.queryByText(/overwritten/)).not.toBeInTheDocument();
    await vi.waitFor(() => expect(onImport).toHaveBeenCalledWith([{ name: "Review", body: "New body" }], true));
    expect(await screen.findByText(/Imported 1, skipped 0/)).toBeInTheDocument();
  });

  it("filters templates in list mode using the search input", async () => {
    const templates = [
      { id: "t1", name: "PR Review", body: "Review the PR at ${URL}" },
      { id: "t2", name: "Set Goal", body: "Set a goal for requirements" },
      { id: "t3", name: "Worker Anxiety", body: "Create recurring reminder" },
    ];
    render(<PromptTemplateDialog {...base} templates={templates} />);

    expect(screen.getByText("PR Review")).toBeInTheDocument();
    expect(screen.getByText("Set Goal")).toBeInTheDocument();
    expect(screen.getByText("Worker Anxiety")).toBeInTheDocument();

    const searchInput = screen.getByRole("searchbox", { name: "Search prompt templates" });
    fireEvent.change(searchInput, { target: { value: "goal" } });

    expect(screen.queryByText("PR Review")).not.toBeInTheDocument();
    expect(screen.getByText("Set Goal")).toBeInTheDocument();
    expect(screen.queryByText("Worker Anxiety")).not.toBeInTheDocument();

    fireEvent.change(searchInput, { target: { value: "nomatch" } });
    expect(screen.getByText("No matching prompt templates")).toBeInTheDocument();
  });
  it("previews tokens and inserts at the captured selection", async () => { const user = userEvent.setup(); const onInsert = vi.fn(); render(<PromptTemplateDialog {...base} templateToUse={template} selection={{ start: 2, end: 2 }} onInsert={onInsert} />); fireEvent.change(screen.getByRole("textbox", { name: "file" }), { target: { value: "app.ts" } }); expect(screen.getByText("Review app.ts")).toBeInTheDocument(); await user.click(screen.getByRole("button", { name: "Paste" })); expect(onInsert).toHaveBeenCalledWith("Review app.ts", { start: 2, end: 2 }); });

  it("renders clean icon-only action-buttons with tooltips in toolbar", () => {
    const { container } = render(<PromptTemplateDialog {...base} />);
    const importBtn = screen.getByTitle("Import templates");
    expect(importBtn).toHaveClass("action-button");
    expect(importBtn.querySelector(".fa-file-import")).toBeInTheDocument();

    const exportBtn = screen.getByTitle("Export templates");
    expect(exportBtn).toHaveClass("action-button");
    expect(exportBtn.querySelector(".fa-file-export")).toBeInTheDocument();

    const addBtn = screen.getByTitle("New template");
    expect(addBtn).toHaveClass("action-button");
    expect(addBtn.querySelector(".fa-plus")).toBeInTheDocument();
  });
});
