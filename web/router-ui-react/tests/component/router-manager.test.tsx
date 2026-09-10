import { describe, expect, it, vi } from "vitest";
import type { ComponentProps } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { RouterManagerDialog } from "../../src/components/RouterManagerDialog";
import type { SavedRouter } from "../../src/domain/router-manager";

afterEach(cleanup);

const routers: SavedRouter[] = [{ url: "http://one.test", alias: "One", agentId: "web-one" }, { url: "https://two.test", agentId: "web-two" }];
const firstRouter = routers[0];
if (!firstRouter) throw new Error("Fixture router is missing");
const props = (overrides: Partial<ComponentProps<typeof RouterManagerDialog>> = {}) => ({ open: true, routers, activeUrl: firstRouter.url, onClose: vi.fn(), onSave: vi.fn(), onSelect: vi.fn(), onRemove: vi.fn(), ...overrides });

describe("RouterManagerDialog", () => {
  it("shows aliases, host fallback, and active selection", () => { render(<RouterManagerDialog {...props()} />); expect(screen.getByRole("dialog", { name: "Routers" })).toHaveClass("message-dialog", "router-dialog"); expect(screen.getByText("One")).toBeInTheDocument(); expect(screen.getByText("two.test")).toBeInTheDocument(); expect(screen.getByText(/selected/)).toBeInTheDocument(); });
  it("normalizes and saves a new router", async () => { const user = userEvent.setup(); const onSave = vi.fn(); render(<RouterManagerDialog {...props({ routers: [firstRouter], onSave })} />); await user.click(screen.getByLabelText("Add router")); await user.clear(screen.getByPlaceholderText("http://127.0.0.1:8765")); await user.type(screen.getByPlaceholderText("http://127.0.0.1:8765"), "new.test/path"); const save = document.querySelector(".router-form-actions button:last-child"); expect(save).not.toBeNull(); if (!(save instanceof HTMLButtonElement)) throw new Error("Save button is missing"); await user.click(save); expect(onSave).toHaveBeenCalledWith({ url: "http://new.test", agentId: "wolfgang" }); });
  it("cannot remove the final saved router", () => { render(<RouterManagerDialog {...props({ routers: [firstRouter] })} />); expect(screen.getByLabelText("Remove one.test")).toBeDisabled(); });
});
