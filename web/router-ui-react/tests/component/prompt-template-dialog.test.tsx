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

  describe("GitHub Sync", () => {
    it("renders github sync controls when client is provided", async () => {
      const mockClient = {
        getGitSyncTarget: vi.fn().mockResolvedValue({ target: "prompt_templates", repo_url: "git@github.com:foo/bar.git" }),
        setGitSyncTarget: vi.fn(),
        pullGitSync: vi.fn(),
        pushGitSync: vi.fn(),
      } as any;

      render(<PromptTemplateDialog {...base} client={mockClient} />);

      expect(screen.getByRole("region", { name: "GitHub Sync" })).toBeInTheDocument();
      await vi.waitFor(() => {
        expect(screen.getByDisplayValue("git@github.com:foo/bar.git")).toBeInTheDocument();
      });
      expect(screen.getByRole("button", { name: /Pull from GitHub/i })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /Push to GitHub/i })).toBeInTheDocument();
    });

    it("saves repository URL on save button click", async () => {
      const user = userEvent.setup();
      const mockClient = {
        getGitSyncTarget: vi.fn().mockResolvedValue(null),
        setGitSyncTarget: vi.fn().mockResolvedValue({ target: "prompt_templates", repo_url: "git@github.com:new/repo.git" }),
        pullGitSync: vi.fn(),
        pushGitSync: vi.fn(),
      } as any;

      render(<PromptTemplateDialog {...base} client={mockClient} />);

      const input = screen.getByPlaceholderText(/git@github.com:owner\/repo.git/);
      await user.type(input, "git@github.com:new/repo.git");

      const saveBtn = screen.getByRole("button", { name: "Save" });
      expect(saveBtn).not.toBeDisabled();
      await user.click(saveBtn);

      expect(mockClient.setGitSyncTarget).toHaveBeenCalledWith("prompt_templates", "git@github.com:new/repo.git");
      expect(await screen.findByText("Repository URL saved.")).toBeInTheDocument();
    });

    it("prompts confirmation when Pull is clicked, and warns when file does not exist", async () => {
      const user = userEvent.setup();
      const mockClient = {
        getGitSyncTarget: vi.fn().mockResolvedValue({ target: "prompt_templates", repo_url: "git@github.com:foo/bar.git" }),
        setGitSyncTarget: vi.fn(),
        pullGitSync: vi.fn().mockResolvedValue({ exists: false }),
        pushGitSync: vi.fn(),
      } as any;

      render(<PromptTemplateDialog {...base} client={mockClient} />);

      await vi.waitFor(() => {
        expect(screen.getByDisplayValue("git@github.com:foo/bar.git")).toBeInTheDocument();
      });

      const pullBtn = screen.getByRole("button", { name: /Pull from GitHub/i });
      await user.click(pullBtn);

      // Destructive confirmation prompt appears
      expect(screen.getByText(/This will replace all local prompt templates with the version from GitHub. Continue\?/i)).toBeInTheDocument();

      const confirmBtn = screen.getByRole("button", { name: "Confirm Pull" });
      await user.click(confirmBtn);

      expect(mockClient.pullGitSync).toHaveBeenCalledWith("prompt_templates");
      // Warning popup / toast appears
      expect(await screen.findByText("No prompt-templates.json found in this repository. Nothing was changed.")).toBeInTheDocument();
    });

    it("calls onReloadTemplates and shows count on successful pull", async () => {
      const user = userEvent.setup();
      const onReloadTemplates = vi.fn();
      const mockClient = {
        getGitSyncTarget: vi.fn().mockResolvedValue({ target: "prompt_templates", repo_url: "git@github.com:foo/bar.git" }),
        setGitSyncTarget: vi.fn(),
        pullGitSync: vi.fn().mockResolvedValue({ exists: true, count: 5 }),
        pushGitSync: vi.fn(),
      } as any;

      render(<PromptTemplateDialog {...base} client={mockClient} onReloadTemplates={onReloadTemplates} />);

      await vi.waitFor(() => {
        expect(screen.getByDisplayValue("git@github.com:foo/bar.git")).toBeInTheDocument();
      });

      const pullBtn = screen.getByRole("button", { name: /Pull from GitHub/i });
      await user.click(pullBtn);

      const confirmBtn = screen.getByRole("button", { name: "Confirm Pull" });
      await user.click(confirmBtn);

      expect(mockClient.pullGitSync).toHaveBeenCalledWith("prompt_templates");
      expect(await screen.findByText("Successfully pulled 5 templates from GitHub.")).toBeInTheDocument();
      expect(onReloadTemplates).toHaveBeenCalled();
    });

    it("pushes to github and shows success message", async () => {
      const user = userEvent.setup();
      const mockClient = {
        getGitSyncTarget: vi.fn().mockResolvedValue({ target: "prompt_templates", repo_url: "git@github.com:foo/bar.git" }),
        setGitSyncTarget: vi.fn(),
        pullGitSync: vi.fn(),
        pushGitSync: vi.fn().mockResolvedValue({ success: true, count: 1 }),
      } as any;

      render(<PromptTemplateDialog {...base} client={mockClient} />);

      await vi.waitFor(() => {
        expect(screen.getByDisplayValue("git@github.com:foo/bar.git")).toBeInTheDocument();
      });

      const pushBtn = screen.getByRole("button", { name: /Push to GitHub/i });
      await user.click(pushBtn);

      expect(mockClient.pushGitSync).toHaveBeenCalledWith("prompt_templates");
      expect(await screen.findByText("Successfully pushed 1 template to GitHub.")).toBeInTheDocument();
    });

    it("clears repository URL when clear button is clicked", async () => {
      const user = userEvent.setup();
      const mockClient = {
        getGitSyncTarget: vi.fn().mockResolvedValue({ target: "prompt_templates", repo_url: "git@github.com:foo/bar.git" }),
        setGitSyncTarget: vi.fn(),
        clearGitSyncTarget: vi.fn().mockResolvedValue(undefined),
        pullGitSync: vi.fn(),
        pushGitSync: vi.fn(),
      } as any;

      render(<PromptTemplateDialog {...base} client={mockClient} />);

      await vi.waitFor(() => {
        expect(screen.getByDisplayValue("git@github.com:foo/bar.git")).toBeInTheDocument();
      });

      const clearBtn = screen.getByRole("button", { name: "Clear repository URL" });
      expect(clearBtn).toBeInTheDocument();
      await user.click(clearBtn);

      expect(mockClient.clearGitSyncTarget).toHaveBeenCalledWith("prompt_templates");
      expect(await screen.findByText("Repository URL cleared.")).toBeInTheDocument();
      expect(screen.getByPlaceholderText(/git@github.com:owner\/repo.git/)).toHaveValue("");
    });
  });
});

