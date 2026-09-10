import { describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { ModelPicker } from "../../src/components/ModelPicker";
import type { ModelSummary } from "../../src/domain/models";

afterEach(cleanup);

const models: ModelSummary[] = [{ provider: "openai", id: "gpt-5", name: "GPT 5" }, { provider: "anthropic", id: "claude" }];
const firstModel = models[0];
if (!firstModel) throw new Error("Fixture model is missing");

describe("ModelPicker", () => {
  it("uses the legacy Font Awesome caret", () => {
    render(<ModelPicker models={models} onChange={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Select model" }).querySelector("i")).toHaveClass("fa-solid", "fa-chevron-down");
  });

  it("searches the catalog and reports truncation", async () => { const user = userEvent.setup(); render(<ModelPicker models={models} currentModel={firstModel} totalCount={10} truncated onChange={vi.fn()} />); await user.click(screen.getByRole("button", { name: "openai/gpt-5" })); expect(screen.getByText("Showing 2 of 10 models.")).toBeInTheDocument(); await user.type(screen.getByRole("searchbox"), "claude"); expect(screen.getByRole("option", { name: /claude/ })).toBeInTheDocument(); expect(screen.queryByRole("option", { name: /GPT 5/ })).not.toBeInTheDocument(); });
  it("returns the selected model and restores focus", async () => { const user = userEvent.setup(); const onChange = vi.fn(); render(<ModelPicker models={models} onChange={onChange} />); const button = screen.getByRole("button", { name: "Select model" }); await user.click(button); await user.click(screen.getByRole("option", { name: /GPT 5/ })); expect(onChange).toHaveBeenCalledWith(firstModel); expect(document.activeElement).toBe(button); });
  it("offers the router default", async () => {
    const user = userEvent.setup();
    const onDefault = vi.fn();
    render(<ModelPicker models={models} onChange={vi.fn()} emptyOption={{ label: "Use router default", description: "Uses the router default", onSelect: onDefault }} />);
    const button = screen.getByRole("button", { name: "Use router default" });
    await user.click(button);
    expect(button).toHaveAttribute("aria-haspopup", "dialog");
    await user.click(screen.getByRole("option", { name: /Use router default/ }));
    expect(onDefault).toHaveBeenCalledOnce();
  });

  it("closes with Escape and restores trigger focus", async () => {
    const user = userEvent.setup();
    render(<ModelPicker models={models} onChange={vi.fn()} />);
    const button = screen.getByRole("button", { name: "Select model" });
    await user.click(button);
    const search = await screen.findByRole("searchbox");
    await waitFor(() => expect(document.activeElement).toBe(search));
    fireEvent.keyDown(search, { key: "Escape" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(document.activeElement).toBe(button);
  });

  it("uses dialog-local absolute coordinates so a native dialog picker stays in the viewport", async () => {
    const user = userEvent.setup();
    const originalWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
    const originalHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1024 });
    Object.defineProperty(window, "innerHeight", { configurable: true, value: 725 });
    try {
      const { container } = render(<dialog open><ModelPicker models={models} onChange={vi.fn()} /></dialog>);
      const dialog = container.querySelector("dialog");
      const button = container.querySelector<HTMLButtonElement>(".model-current");
      if (!dialog || !button) throw new Error("model picker dialog is missing");
      vi.spyOn(dialog, "getBoundingClientRect").mockReturnValue({ left: 200, top: 50, right: 880, bottom: 700, width: 680, height: 650, x: 200, y: 50, toJSON: () => ({}) });
      vi.spyOn(button, "getBoundingClientRect").mockReturnValue({ left: 400, top: 640, right: 620, bottom: 674, width: 220, height: 34, x: 400, y: 640, toJSON: () => ({}) });
      await user.click(button);
      const menu = screen.getByRole("dialog", { name: "Choose agent model" });
      expect(menu.parentElement).toBe(dialog);
      expect(menu).toHaveStyle({ position: "absolute", top: "auto", left: "60px", bottom: "68px" });
    } finally {
      if (originalWidth) Object.defineProperty(window, "innerWidth", originalWidth);
      if (originalHeight) Object.defineProperty(window, "innerHeight", originalHeight);
    }
  });

  it("keeps the main picker body-portalled and viewport-fixed", async () => {
    const user = userEvent.setup();
    render(<ModelPicker models={models} onChange={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Select model" }));
    const menu = screen.getByRole("dialog", { name: "Choose agent model" });
    expect(menu.parentElement).toBe(document.body);
    expect(menu).toHaveStyle({ position: "fixed" });
  });
});
