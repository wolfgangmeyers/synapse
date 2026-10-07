import "@testing-library/jest-dom/vitest";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sidebar, type SidebarProps } from "../../src/components/Sidebar";
import { RECENT_AGENTS_KEY, RECENT_AGENTS_LIMIT } from "../../src/persistence/recent-agents";
import { ownerScope, type StorageAdapter } from "../../src/persistence/settings-repository";
import type { Agent } from "../../src/state/agent-projection";

class MemoryStorage implements StorageAdapter {
  readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
}
const agents: Agent[] = [{ agent_id: "zebra" }, { agent_id: "alpha.bot" }, { agent_id: "beta_worker" }];
const search = (): HTMLElement => screen.getByRole("searchbox", { name: "Filter connected agents" });
const rowIds = (): (string | null)[] => screen.getAllByRole("listitem").map((row) => row.getAttribute("data-agent-id"));
const select = (name: string): void => { fireEvent.click(screen.getByRole("button", { name })); };
const scope = { routerUrl: "http://router.test", agentId: "web" };
const scopeKey = ownerScope(scope.routerUrl, scope.agentId);
const stored = (recent: string[]): string => JSON.stringify({ [scopeKey]: recent });
const sidebar = (props: Partial<SidebarProps> = {}): React.ReactElement => <Sidebar scope={scope} routerLabel="local" connection="connected" agents={agents} {...props} />;
beforeEach(() => { vi.stubGlobal("localStorage", new MemoryStorage()); });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("Sidebar recent agents", () => {
  it.each(["ALPHA.BOT", "  ALPHA BOT  "])("keeps plain search, name filtering and filtered count for %s", (query) => {
    const onFilterChange = vi.fn();
    const { container } = render(sidebar({ onFilterChange }));
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Select an agent" })).not.toBeInTheDocument();
    fireEvent.change(search(), { target: { value: query } });
    expect(onFilterChange).toHaveBeenLastCalledWith(query);
    expect(rowIds()).toEqual(["alpha.bot"]);
    expect(container.querySelector(".section-title small")).toHaveTextContent("1");
    select("Alpha Bot (alpha.bot)");
    expect(search()).toHaveValue("");
    expect(onFilterChange).toHaveBeenLastCalledWith("");
    expect(rowIds()).toEqual(["alpha.bot", "beta_worker", "zebra"]);
    expect(container.querySelector(".section-title small")).toHaveTextContent("3");
    fireEvent.change(search(), { target: { value: "missing" } });
    expect(screen.getByText("No connected agents")).toBeInTheDocument();
    expect(container.querySelector(".section-title small")).toHaveTextContent("0");
  });

  it("does not focus a row merely because persisted recency loads", () => {
    const storage = new MemoryStorage();
    storage.setItem(RECENT_AGENTS_KEY, stored(["tmux:zebra"]));
    render(sidebar({ storage }));
    expect(rowIds()[0]).toBe("zebra");
    expect(screen.getByRole("button", { name: "Zebra (zebra)" })).not.toHaveFocus();
  });

  it("moves selected agents first, resets the list scroll and persists recency through refresh", () => {
    const storage = new MemoryStorage();
    const onSelectAgent = vi.fn();
    localStorage.setItem(RECENT_AGENTS_KEY, stored(["tmux:beta_worker"]));
    const view = render(sidebar({ storage, onSelectAgent }));
    expect(rowIds()).toEqual(["alpha.bot", "beta_worker", "zebra"]);
    const list = screen.getByRole("list");
    list.scrollTop = 500;
    select("Zebra (zebra)");
    expect(onSelectAgent).toHaveBeenCalledWith("tmux:zebra");
    expect(list.scrollTop).toBe(0);
    expect(rowIds()).toEqual(["zebra", "alpha.bot", "beta_worker"]);
    select("Beta Worker (beta_worker)");
    expect(rowIds()).toEqual(["beta_worker", "zebra", "alpha.bot"]);
    select("Zebra (zebra)");
    expect(storage.getItem(RECENT_AGENTS_KEY)).toBe(stored(["tmux:zebra", "tmux:beta_worker"]));
    expect(localStorage.getItem(RECENT_AGENTS_KEY)).toBe(stored(["tmux:beta_worker"]));
    list.scrollTop = 200;
    select("Zebra (zebra)");
    expect(list.scrollTop).toBe(0);
    view.unmount();
    render(sidebar({ storage }));
    expect(rowIds()).toEqual(["zebra", "beta_worker", "alpha.bot"]);
    fireEvent.change(search(), { target: { value: "a" } });
    expect(rowIds()).toEqual(["zebra", "beta_worker", "alpha.bot"]);
    fireEvent.change(search(), { target: { value: "alpha" } });
    expect(rowIds()).toEqual(["alpha.bot"]);
  });

  it("clears a controlled filter, promotes the pick and scrolls to top in the same interaction", () => {
    const onSelectAgent = vi.fn();
    const onFilterChange = vi.fn();
    function ControlledSidebar(): React.ReactElement {
      const [filter, setFilter] = useState("ZEBRA");
      const [selectedAgentId, setSelectedAgentId] = useState<string>();
      return sidebar({ filter, selectedAgentId, onFilterChange: (value) => { setFilter(value); onFilterChange(value); }, onSelectAgent: (key) => { setSelectedAgentId(key); onSelectAgent(key); } });
    }
    render(<ControlledSidebar />);
    expect(rowIds()).toEqual(["zebra"]);
    screen.getByRole("list").scrollTop = 400;
    select("Zebra (zebra)");
    expect(search()).toHaveValue("");
    expect(rowIds()).toEqual(["zebra", "alpha.bot", "beta_worker"]);
    expect(screen.getAllByRole("listitem")[0]).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("list").scrollTop).toBe(0);
    expect(onFilterChange).toHaveBeenCalledWith("");
    expect(onSelectAgent).toHaveBeenCalledWith("tmux:zebra");
  });

  it("retains source-qualified selection and ordering with duplicate raw IDs", () => {
    const onSelectAgent = vi.fn();
    const duplicates: Agent[] = [{ agent_id: "worker", source: "tmux" }, { agent_id: "worker", source: "router-managed" }];
    const view = render(sidebar({ agents: duplicates, selectedAgentId: "tmux:worker", onSelectAgent }));
    const tmuxRow = screen.getAllByRole("listitem").find((row) => row.textContent?.includes("tmux"));
    const managedRow = screen.getAllByRole("listitem").find((row) => row.textContent?.includes("RPC"));
    expect(tmuxRow).toHaveAttribute("aria-selected", "true");
    expect(managedRow).toHaveAttribute("aria-selected", "false");
    if (!tmuxRow || !managedRow) throw new Error("Missing source-qualified rows");
    fireEvent.click(within(managedRow).getByRole("button"));
    expect(screen.getAllByRole("listitem")[0]).toBe(managedRow);
    fireEvent.click(within(tmuxRow).getByRole("button"));
    expect(screen.getAllByRole("listitem")[0]).toBe(tmuxRow);
    expect(onSelectAgent.mock.calls).toEqual([["router-managed:worker"], ["tmux:worker"]]);
    expect(localStorage.getItem(RECENT_AGENTS_KEY)).toBe(stored(["tmux:worker", "router-managed:worker"]));
    view.unmount();
    render(sidebar({ agents: duplicates }));
    expect(screen.getAllByRole("listitem")[0]).toHaveTextContent("tmux");
    expect(screen.getAllByRole("listitem")[1]).toHaveTextContent("RPC");
  });

  it("bounds recent picks without truncating connected agents", () => {
    const many = Array.from({ length: 20 }, (_, i) => ({ agent_id: `worker-${String(i).padStart(2, "0")}` }));
    render(sidebar({ agents: many }));
    for (const agent of many) select(`Worker ${agent.agent_id.slice(-2)} (${agent.agent_id})`);
    expect(JSON.parse(localStorage.getItem(RECENT_AGENTS_KEY) ?? "null")).toEqual({ [scopeKey]: many.slice(-RECENT_AGENTS_LIMIT).reverse().map((agent) => `tmux:${agent.agent_id}`) });
    expect(screen.getAllByRole("listitem")).toHaveLength(20);
    expect(rowIds()).toEqual([...many.slice(-10).reverse(), ...many.slice(0, 10)].map((agent) => agent.agent_id));
    expect(screen.getByRole("list")).toHaveClass("agents");
  });

  it("supports keyboard selection as rows move", async () => {
    const user = userEvent.setup();
    const onSelectAgent = vi.fn();
    render(sidebar({ onSelectAgent }));
    const zebra = screen.getByRole("button", { name: "Zebra (zebra)" });
    zebra.focus();
    screen.getByRole("list").scrollTop = 200;
    await user.keyboard("{Enter}");
    expect(rowIds()[0]).toBe("zebra");
    expect(screen.getByRole("list").scrollTop).toBe(0);
    const beta = screen.getByRole("button", { name: "Beta Worker (beta_worker)" });
    beta.focus();
    await user.keyboard(" ");
    expect(rowIds()[0]).toBe("beta_worker");
    expect(onSelectAgent.mock.calls).toEqual([["tmux:zebra"], ["tmux:beta_worker"]]);
  });

  it("preserves controlled filtering and row actions without treating actions as picks", () => {
    const onFilterChange = vi.fn();
    const onSelectAgent = vi.fn();
    const onGoal = vi.fn();
    const actionAgents = agents.map((agent) => ({ ...agent, goal: { enabled: true, goal: "Task goal" } }));
    const view = render(sidebar({ agents: actionAgents, filter: "beta", onFilterChange, onSelectAgent, onGoal }));
    fireEvent.change(search(), { target: { value: "alpha" } });
    expect(onFilterChange).toHaveBeenCalledWith("alpha");
    expect(rowIds()).toEqual(["beta_worker"]);
    view.rerender(sidebar({ agents: actionAgents, filter: "alpha", onFilterChange, onSelectAgent, onGoal }));
    expect(rowIds()).toEqual(["alpha.bot"]);
    screen.getByRole("list").scrollTop = 200;
    fireEvent.click(screen.getByRole("button", { name: "Set goal" }));
    expect(onGoal).toHaveBeenCalledWith("tmux:alpha.bot");
    expect(onSelectAgent).not.toHaveBeenCalled();
    expect(screen.getByRole("list").scrollTop).toBe(200);
    expect(localStorage.getItem(RECENT_AGENTS_KEY)).toBeNull();
  });

  it("ignores disconnected picks until they reconnect, without forgetting their recency", () => {
    localStorage.setItem(RECENT_AGENTS_KEY, stored(["tmux:zebra", "tmux:offline", "tmux:beta_worker"]));
    const view = render(sidebar());
    expect(rowIds()).toEqual(["zebra", "beta_worker", "alpha.bot"]);
    view.rerender(sidebar({ agents: agents.filter((agent) => agent.agent_id !== "zebra") }));
    expect(rowIds()).toEqual(["beta_worker", "alpha.bot"]);
    view.rerender(sidebar());
    expect(rowIds()).toEqual(["zebra", "beta_worker", "alpha.bot"]);
  });

  it.each([
    { routerUrl: "http://other-router.test", agentId: "web" },
    { routerUrl: scope.routerUrl, agentId: "other-owner" },
  ])("reloads recency when the router or owner changes to %j", (otherScope) => {
    const view = render(sidebar());
    select("Zebra (zebra)");
    view.rerender(sidebar({ scope: otherScope }));
    expect(rowIds()).toEqual(["alpha.bot", "beta_worker", "zebra"]);
    select("Beta Worker (beta_worker)");
    expect(rowIds()).toEqual(["beta_worker", "alpha.bot", "zebra"]);
    view.rerender(sidebar());
    expect(rowIds()).toEqual(["zebra", "alpha.bot", "beta_worker"]);
    view.rerender(sidebar({ scope: otherScope }));
    expect(rowIds()).toEqual(["beta_worker", "alpha.bot", "zebra"]);
  });

  it("keeps session recency and selection working when reads or writes fail", () => {
    const storage: StorageAdapter = { getItem() { throw new Error("unavailable"); }, setItem() { throw new Error("quota"); }, removeItem: vi.fn() };
    const onSelectAgent = vi.fn();
    render(sidebar({ storage, onSelectAgent }));
    select("Zebra (zebra)");
    expect(rowIds()).toEqual(["zebra", "alpha.bot", "beta_worker"]);
    expect(onSelectAgent).toHaveBeenCalledWith("tmux:zebra");
  });

  it("works when accessing browser storage itself is unavailable", () => {
    Object.defineProperty(globalThis, "localStorage", { configurable: true, get() { throw new Error("disabled"); } });
    render(sidebar());
    select("Zebra (zebra)");
    expect(rowIds()[0]).toBe("zebra");
  });
});
