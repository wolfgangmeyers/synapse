import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { PromptControls } from "../../src/components/PromptControls";
import type { PromptTemplate } from "../../src/domain/prompt-templates";

afterEach(cleanup);

const templates: PromptTemplate[] = Array.from({ length: 20 }, (_, index) => ({
  id: `template-${index + 1}`,
  name: `Template ${index + 1}`,
  body: `Prompt body ${index + 1}`,
}));

describe("PromptControls", () => {
  it("renders every prompt template when the list contains more than ten templates", async () => {
    const user = userEvent.setup();
    render(<PromptControls templates={templates} onManage={vi.fn()} onUse={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Select prompt" }));

    const options = screen.getAllByRole("option");
    expect(options).toHaveLength(templates.length);
    templates.forEach((template) => {
      const card = screen.getByRole("option", { name: `Use prompt template ${template.name}` });
      expect(card).toHaveClass("prompt-card");
      expect(card).toHaveTextContent(template.body);
    });
  });

  it("opens as an accessible dialog with close button and Esc support", async () => {
    const user = userEvent.setup();
    render(<PromptControls templates={templates} onManage={vi.fn()} onUse={vi.fn()} />);

    const picker = screen.getByRole("button", { name: "Select prompt" });
    expect(picker).toHaveAttribute("aria-haspopup", "dialog");
    expect(picker).toHaveAttribute("aria-expanded", "false");

    await user.click(picker);
    expect(picker).toHaveAttribute("aria-expanded", "true");

    const dialog = screen.getByRole("dialog", { name: "Choose prompt template" });
    expect(dialog).toBeInTheDocument();
    expect(dialog).toHaveClass("prompt-picker-dialog");

    // Close button ("Close prompt picker")
    const closeBtn = screen.getByRole("button", { name: "Close prompt picker" });
    await user.click(closeBtn);

    expect(screen.queryByRole("dialog", { name: "Choose prompt template" })).not.toBeInTheDocument();
    expect(picker).toHaveAttribute("aria-expanded", "false");

    // Reopen and test Escape key closes dialog (DialogShell listens on cancel or close)
    await user.click(picker);
    const dialogElement = screen.getByRole("dialog", { name: "Choose prompt template" });
    expect(dialogElement).toBeInTheDocument();

    // In JSDOM HTMLDialogElement, native cancel event corresponds to Escape
    fireEvent(dialogElement, new Event("cancel", { cancelable: true }));
    expect(screen.queryByRole("dialog", { name: "Choose prompt template" })).not.toBeInTheDocument();
    expect(picker).toHaveAttribute("aria-expanded", "false");
  });

  it("sorts prompt templates by recency (MRU) with usage record", async () => {
    const user = userEvent.setup();
    const customTemplates: PromptTemplate[] = [
      { id: "t1", name: "Alpha Template", body: "Body A" },
      { id: "t2", name: "Beta Template", body: "Body B" },
      { id: "t3", name: "Gamma Template", body: "Body C" },
    ];
    // Beta is most recently used (highest timestamp), then Gamma, then Alpha (0)
    const usage = { t2: 5000, t3: 3000, t1: 1000 };

    render(<PromptControls templates={customTemplates} usage={usage} onManage={vi.fn()} onUse={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Select prompt" }));

    const cards = screen.getAllByRole("option");
    expect(cards[0]).toHaveTextContent("Beta Template");
    expect(cards[1]).toHaveTextContent("Gamma Template");
    expect(cards[2]).toHaveTextContent("Alpha Template");
  });

  it("passes the selected template to onUse and restores focus to the picker", async () => {
    const user = userEvent.setup();
    const onUse = vi.fn();
    render(<PromptControls templates={templates} onManage={vi.fn()} onUse={onUse} />);

    const picker = screen.getByRole("button", { name: "Select prompt" });
    await user.click(picker);
    await user.click(screen.getByRole("option", { name: "Use prompt template Template 20" }));

    expect(onUse).toHaveBeenCalledOnce();
    expect(onUse).toHaveBeenCalledWith(templates[19]);
    expect(picker).toHaveAttribute("aria-expanded", "false");
    expect(document.activeElement).toBe(picker);
  });

  it("filters templates by name and body using the search input", async () => {
    const user = userEvent.setup();
    const customTemplates: PromptTemplate[] = [
      { id: "t1", name: "PR Review", body: "Review the PR at ${URL}" },
      { id: "t2", name: "Bug Fix", body: "Fix issue in module" },
      { id: "t3", name: "Documentation", body: "Write PR summary and notes" },
    ];
    render(<PromptControls templates={customTemplates} onManage={vi.fn()} onUse={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Select prompt" }));
    const searchInput = screen.getByRole("searchbox", { name: "Search prompt templates" });
    expect(searchInput).toBeInTheDocument();

    await user.type(searchInput, "Bug");
    expect(screen.getAllByRole("option")).toHaveLength(1);
    expect(screen.getByRole("option", { name: "Use prompt template Bug Fix" })).toBeInTheDocument();

    await user.clear(searchInput);
    await user.type(searchInput, "PR");
    // Matches "PR Review" (by name) and "Documentation" (by body "Write PR summary...")
    expect(screen.getAllByRole("option")).toHaveLength(2);

    await user.clear(searchInput);
    await user.type(searchInput, "nonexistent query");
    expect(screen.getByText("No matching prompt templates")).toBeInTheDocument();
  });
});
