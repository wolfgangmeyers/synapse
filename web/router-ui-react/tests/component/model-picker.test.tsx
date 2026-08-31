import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
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
});
