import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sidebar } from "../../src/components/Sidebar";

afterEach(cleanup);

describe("Sidebar", () => {
  it("filters and selects agents", () => {
    const onSelect = vi.fn();
    render(<Sidebar routerLabel="local" connection="connected" agents={[{ agent_id: "alpha.bot" }, { agent_id: "beta" }]} onSelectAgent={onSelect} />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "alpha" } });
    expect(screen.getByRole("button", { name: /Alpha Bot/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Beta/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Alpha Bot/i }));
    expect(onSelect).toHaveBeenCalledWith("tmux:alpha.bot");
  });

  it("keeps managed actions in an accessible menu beside the connection badge", async () => {
    const user = userEvent.setup();
    const onLaunchManaged = vi.fn();
    const onRestartAllManaged = vi.fn();
    render(<Sidebar routerLabel="local" connection="connected" connectionIdentity="wolfgang" agents={[]} onLaunchManaged={onLaunchManaged} onRestartAllManaged={onRestartAllManaged} />);

    const connection = screen.getByLabelText("Connection status");
    const menuButton = screen.getByRole("button", { name: "Managed actions" });
    expect(connection.parentElement).toContainElement(menuButton);
    expect(menuButton).toHaveAttribute("aria-haspopup", "menu");
    expect(menuButton).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu", { name: "Managed actions" })).not.toBeInTheDocument();
    expect(screen.queryByText("Launch managed agent")).not.toBeInTheDocument();

    await user.click(menuButton);
    expect(menuButton).toHaveAttribute("aria-expanded", "true");
    const menu = screen.getByRole("menu", { name: "Managed actions" });
    expect(menu).toHaveTextContent("Launch managed agent");
    expect(menu).toHaveTextContent("Restart all managed");
    expect(menu).not.toHaveTextContent("History");

    await user.keyboard("{Escape}");
    expect(menuButton).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu", { name: "Managed actions" })).not.toBeInTheDocument();

    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Launch managed agent" }));
    expect(onLaunchManaged).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu", { name: "Managed actions" })).not.toBeInTheDocument();

    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Restart all managed" }));
    expect(onRestartAllManaged).toHaveBeenCalledOnce();

  });
});
