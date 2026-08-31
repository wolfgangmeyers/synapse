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

  it("creates, exports, and confirms an import conflict", async () => { const user = userEvent.setup(); const onExport = vi.fn(); const onImport = vi.fn(); vi.stubGlobal("URL", { createObjectURL: vi.fn(() => "blob:prompt-templates"), revokeObjectURL: vi.fn() }); vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined); render(<PromptTemplateDialog {...base} onExport={onExport} onImport={onImport} />); await user.click(screen.getByTestId("prompt-export")); expect(onExport).toHaveBeenCalledWith(expect.objectContaining({ format: "mecha-prompt-templates", version: 1 })); const input = document.querySelector("input[type='file']"); expect(input).not.toBeNull(); if (!(input instanceof HTMLInputElement)) throw new Error("Import input is missing"); const file = new File([JSON.stringify({ format: "mecha-prompt-templates", version: 1, templates: [{ id: "t1", name: "Review", body: "New body", updated_at: "2026-08-25T12:34:56.789Z" }] })], "templates.json", { type: "application/json" }); fireEvent.change(input, { target: { files: [file] } }); expect(await screen.findByText(/overwritten/)).toBeInTheDocument(); await user.click(screen.getByRole("button", { name: "Import and overwrite" })); expect(onImport).toHaveBeenCalledWith([{ id: "t1", name: "Review", body: "New body", updated_at: "2026-08-25T12:34:56.789Z" }], true); });
  it("previews tokens and inserts at the captured selection", async () => { const user = userEvent.setup(); const onInsert = vi.fn(); render(<PromptTemplateDialog {...base} templateToUse={template} selection={{ start: 2, end: 2 }} onInsert={onInsert} />); fireEvent.change(screen.getByRole("textbox", { name: "file" }), { target: { value: "app.ts" } }); expect(screen.getByText("Review app.ts")).toBeInTheDocument(); await user.click(screen.getByRole("button", { name: "Paste" })); expect(onInsert).toHaveBeenCalledWith("Review app.ts", { start: 2, end: 2 }); });
});
