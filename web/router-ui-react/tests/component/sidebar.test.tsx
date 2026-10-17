import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sidebar } from "../../src/components/Sidebar";

afterEach(cleanup);

describe("Sidebar", () => {
  it("shows only the accessible brand logo in the branding area", () => {
    const { container } = render(<Sidebar scope={{ routerUrl: "http://router.test", agentId: "web" }} routerLabel="local" connection="connected" agents={[]} />);
    const brand = container.querySelector(".brand");
    expect(brand).toContainElement(screen.getByRole("img", { name: "Mecha Developer" }));
    expect(screen.getByRole("img", { name: "Mecha Developer" })).toHaveAttribute("src", "/logo.png");
    expect(brand?.children).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Refresh agents" })).not.toBeInTheDocument();
    expect(screen.queryByText("Your agent network")).not.toBeInTheDocument();
  });

  it("filters and selects agents", () => {
    const onSelect = vi.fn();
    render(<Sidebar scope={{ routerUrl: "http://router.test", agentId: "web" }} routerLabel="local" connection="connected" agents={[{ agent_id: "alpha.bot" }, { agent_id: "beta" }]} onSelectAgent={onSelect} />);
    const routerBtn = screen.getByRole("button", { name: /local/i });
    expect(routerBtn).toHaveTextContent("local");
    expect(routerBtn).toHaveTextContent("Connected");
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "alpha" } });
    expect(screen.getByRole("button", { name: /Alpha Bot/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Beta/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Alpha Bot/i }));
    expect(onSelect).toHaveBeenCalledWith("tmux:alpha.bot");
  });

  it("does not render a live-update error banner in the sidebar", () => {
    render(<Sidebar scope={{ routerUrl: "http://router.test", agentId: "web" }} routerLabel="local" connection="connected" agents={[]} managedStatus="Live managed-worker updates could not be applied. The router will retry. (Code: UI_LIVE_CONNECTION_FAILED)" />);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("keeps managed actions in an accessible menu beside the connection badge", async () => {
    const user = userEvent.setup();
    const onLaunchManaged = vi.fn();
    const onRestartAllManaged = vi.fn();
    const onProviderSettings = vi.fn();
    render(<Sidebar scope={{ routerUrl: "http://router.test", agentId: "web" }} routerLabel="local" connection="connected" connectionIdentity="wolfgang" agents={[]} onLaunchManaged={onLaunchManaged} onRestartAllManaged={onRestartAllManaged} onProviderSettings={onProviderSettings} />);

    const routerBtn = screen.getByRole("button", { name: /local/i });
    expect(routerBtn).toHaveTextContent("local");
    expect(routerBtn).toHaveTextContent("Connected as wolfgang");
    const menuButton = screen.getByRole("button", { name: "Managed actions" });
    expect(routerBtn.parentElement).toContainElement(menuButton);
    expect(menuButton).toHaveAttribute("aria-haspopup", "menu");
    expect(menuButton).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu", { name: "Managed actions" })).not.toBeInTheDocument();
    expect(screen.queryByText("Launch managed agent")).not.toBeInTheDocument();

    await user.click(menuButton);
    expect(menuButton).toHaveAttribute("aria-expanded", "true");
    const menu = screen.getByRole("menu", { name: "Managed actions" });
    expect(menu).toHaveTextContent("Launch managed agent");
    expect(menu).toHaveTextContent("Restart all managed");
    expect(menu).toHaveTextContent("Provider settings");
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

    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Provider settings" }));
    expect(onProviderSettings).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu", { name: "Managed actions" })).not.toBeInTheDocument();
  });

  it("moves an agent to the top of the visible list when selectedAgentId changes", () => {
    const agents = [
      { agent_id: "alpha.bot", source: "router-managed" as const },
      { agent_id: "beta.bot", source: "router-managed" as const },
      { agent_id: "gamma.bot", source: "router-managed" as const },
    ];
    const { rerender } = render(
      <Sidebar
        scope={{ routerUrl: "http://router.test", agentId: "web" }}
        routerLabel="local"
        connection="connected"
        agents={agents}
      />
    );

    // Initial order is alphabetical: alpha, beta, gamma
    const listItemsBefore = screen.getAllByRole("listitem").map((el) => el.getAttribute("data-agent-id"));
    expect(listItemsBefore).toEqual(["alpha.bot", "beta.bot", "gamma.bot"]);

    // When gamma is selected (e.g. freshly launched)
    rerender(
      <Sidebar
        scope={{ routerUrl: "http://router.test", agentId: "web" }}
        routerLabel="local"
        connection="connected"
        agents={agents}
        selectedAgentId="router-managed:gamma.bot"
      />
    );

    // Gamma should now rise to the top
    const listItemsAfter = screen.getAllByRole("listitem").map((el) => el.getAttribute("data-agent-id"));
    expect(listItemsAfter).toEqual(["gamma.bot", "alpha.bot", "beta.bot"]);
  });
});
