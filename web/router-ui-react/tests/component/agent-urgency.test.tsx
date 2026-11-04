import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AgentRow } from "../../src/components/AgentRow";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import type { Agent } from "../../src/state/agent-projection";
import { readFileSync } from "node:fs";

beforeEach(() => localStorage.setItem("mecha.toolbar.pinnedControls.v2", JSON.stringify(["urgency"])));
afterEach(() => { cleanup(); localStorage.removeItem("mecha.toolbar.pinnedControls.v2"); });
const base: Agent = { agent_id: "worker", source: "router-managed", urgent: false };
const obsolete = '.urgency-control, .urgency-toggle, .urgency-badge, .urgency-rail, .urgency-error, .is-urgent';

describe("tool-driven urgency presentation (URG-001/003/004)", () => {
  it.each([false, undefined])("renders no urgency element or reserved slot for %s, including hover/focus", (urgent) => {
    const agent = urgent === undefined ? { agent_id: "worker" } : { ...base, urgent };
    const { container } = render(<AgentRow agent={agent} />);
    fireEvent.mouseOver(screen.getByRole("listitem"));
    fireEvent.focus(screen.getByRole("button"));
    expect(container.querySelector('[class*="urgency"], [aria-label*="Urgent"], [aria-label*="urgent"]')).toBeNull();
    expect(container.querySelector(obsolete)).toBeNull();
  });

  it("renders a passive urgent badge only for authoritative true", () => {
    const select = vi.fn();
    const { container, rerender } = render(<AgentRow agent={{ ...base, urgent: true }} onSelect={select} />);
    const badge = screen.getByText("Urgent");
    expect(container.querySelectorAll(".agent-row-urgent-badge")).toHaveLength(1);
    expect(container.querySelector(".agent-urgency-marker")).toBeNull();
    expect(badge.hasAttribute("tabindex")).toBe(false);
    expect(badge.hasAttribute("aria-pressed")).toBe(false);
    fireEvent.click(badge);
    expect(select).toHaveBeenCalledWith("worker");
    rerender(<AgentRow agent={{ ...base, urgent: false }} />);
    expect(screen.queryByText("Urgent")).toBeNull();
    expect(container.querySelector(".urgent")).toBeNull();
  });

  it("ships exact-version local Font Awesome CSS, webfonts and license without the CDN", () => {
    const manifest = JSON.parse(readFileSync("package.json", "utf8"));
    expect(manifest.dependencies["@fortawesome/fontawesome-free"]).toBe("6.7.2");
    expect(readFileSync("src/main.tsx", "utf8")).toContain('import "@fortawesome/fontawesome-free/css/all.min.css"');
    expect(readFileSync("index.html", "utf8")).not.toMatch(/cdnjs|font-awesome.*https/);
    const css = readFileSync("node_modules/@fortawesome/fontawesome-free/css/all.min.css", "utf8");
    expect(css).toContain("../webfonts/fa-solid-900.woff2");
    expect(css).toContain("../webfonts/fa-regular-400.woff2");
    expect(css).toContain("../webfonts/fa-brands-400.woff2");
    expect(readFileSync("public/licenses/fontawesome-free-6.7.2.txt", "utf8")).toBe(
      readFileSync("node_modules/@fortawesome/fontawesome-free/LICENSE.txt", "utf8"),
    );
  });

  it("keeps selection, blocked, workflow bolt, compacting and progress intact", () => {
    const { container } = render(<AgentRow selected agent={{ ...base, urgent: true, blocked: true, blocked_reason: "waiting", workflow_state: "review", workflow_name: "Ship", activity_state: "compacting", progress: { percent: 42, note: "checking", updated_at: "2030-01-01T00:00:00Z" } }} />);
    expect(screen.getByRole("listitem").classList.contains("active")).toBe(true);
    expect(screen.getByRole("listitem").classList.contains("compacting")).toBe(true);
    expect(screen.getByText("Urgent")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Blocked: waiting" })).toBeTruthy();
    expect(container.querySelectorAll('.agent-row-workflow-badge .fa-bolt')).toHaveLength(1);
    expect(screen.getByText("Compacting")).toBeTruthy();
    expect(screen.getByText("42%")).toBeTruthy();
    expect(container.querySelector(obsolete)).toBeNull();
  });

  it.each([false, true])("shows the confirmed header toggle when urgent=%s", (urgent) => {
    render(<ConversationHeader agent={{ ...base, urgent }} />);
    const toggle = screen.getByRole("button", { name: urgent ? "Clear urgency" : "Mark urgent" });
    expect(toggle.getAttribute("aria-pressed")).toBe(String(urgent));
    expect(toggle.classList.contains("active")).toBe(urgent);
    expect(toggle.classList.contains("header-action-button--active-teal")).toBe(urgent);
    expect(toggle.hasAttribute("disabled")).toBe(true);
  });
});

describe("urgency operation remount", () => {
  it("does not revive a stale confirmation after switching selection and unmounting", async () => {
    const { createHttpClient } = await import("../../src/transport/http-client");
    const client = createHttpClient("http://switch-unmount-regression");
    const agent: Agent = { agent_id: "switch-worker", urgent: false };
    let finishWrite!: () => void;
    client.agents = vi.fn(async () => ({ agents: [agent] }));
    client.setAgentUrgency = vi.fn(() => new Promise(resolve => { finishWrite = () => resolve(undefined); }));
    const view = render(<ConversationHeader agent={agent} client={client} />);
    fireEvent.click(screen.getByRole("button", { name: "Mark urgent" }));
    await waitFor(() => expect(client.setAgentUrgency).toHaveBeenCalledOnce());
    view.rerender(<ConversationHeader agent={{ agent_id: "other", urgent: false }} client={client} />);
    view.unmount();
    await act(async () => { finishWrite(); });
    // A newer true projection was followed by false while this header was absent.
    const remounted = render(<ConversationHeader agent={{ ...agent, urgent: false }} client={client} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Mark urgent" }).hasAttribute("disabled")).toBe(false));
    expect(client.agents).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Mark urgent" }).getAttribute("aria-pressed")).toBe("false");
    remounted.rerender(<ConversationHeader agent={{ ...agent, urgent: true }} client={client} />);
    expect(screen.getByRole("button", { name: "Clear urgency" }).hasAttribute("disabled")).toBe(false);
  });

  it("does not let a delayed post-write read overwrite a newer projection", async () => {
    const { createHttpClient } = await import("../../src/transport/http-client");
    const client = createHttpClient("http://detached-read-order");
    const agent: Agent = { agent_id: "ordering-worker", urgent: false };
    let finishWrite!: () => void;
    let finishRefresh!: (value: { agents: Agent[] }) => void;
    client.agents = vi.fn().mockResolvedValueOnce({ agents: [agent] }).mockImplementationOnce(() => new Promise(resolve => { finishRefresh = resolve; }));
    client.setAgentUrgency = vi.fn(() => new Promise(resolve => { finishWrite = () => resolve(undefined); }));
    const view = render(<ConversationHeader agent={agent} client={client} />);
    fireEvent.click(screen.getByRole("button", { name: "Mark urgent" }));
    await waitFor(() => expect(client.setAgentUrgency).toHaveBeenCalledOnce());
    view.rerender(<ConversationHeader agent={{ agent_id: "other" }} client={client} />);
    await act(async () => { finishWrite(); });
    await waitFor(() => expect(client.agents).toHaveBeenCalledTimes(2));
    view.rerender(<ConversationHeader agent={{ ...agent, urgent: true }} client={client} />);
    view.rerender(<ConversationHeader agent={{ ...agent, urgent: false }} client={client} />);
    await act(async () => { finishRefresh({ agents: [{ ...agent, urgent: true }] }); });
    expect(screen.getByRole("button", { name: "Mark urgent" }).hasAttribute("disabled")).toBe(false);
  });

  it.each(["success", "rejected", "uncertain", "read failure"] as const)("updates a remounted header after %s", async (outcome) => {
    const { createHttpClient, HttpRequestError } = await import("../../src/transport/http-client");
    const client = createHttpClient(`http://remount-${outcome.replaceAll(" ", "-")}`);
    const agent: Agent = { agent_id: "remount-worker", urgent: false };
    let finishRead!: (value: { agents: Agent[] }) => void;
    let failRead!: (reason: Error) => void;
    let finishWrite!: () => void;
    let failWrite!: (reason: Error) => void;
    client.agents = vi.fn(() => new Promise<{ agents: Agent[] }>((resolve, reject) => { finishRead = resolve; failRead = reject; }));
    client.setAgentUrgency = vi.fn(() => new Promise((resolve, reject) => { finishWrite = () => resolve(undefined); failWrite = reject; }));
    const first = render(<ConversationHeader agent={agent} client={client} />);
    fireEvent.click(screen.getByRole("button", { name: "Mark urgent" }));
    expect(screen.getByRole("button", { name: "Mark urgent" }).hasAttribute("disabled")).toBe(true);
    first.unmount();
    const remounted = render(<ConversationHeader agent={agent} client={client} />);
    expect(screen.getByRole("button", { name: "Mark urgent" }).hasAttribute("disabled")).toBe(true);
    if (outcome === "read failure") {
      failRead(new Error("offline"));
    } else {
      finishRead({ agents: [agent] });
      await waitFor(() => expect(client.setAgentUrgency).toHaveBeenCalledOnce());
      if (outcome === "success") {
        await act(async () => { finishWrite(); });
        await waitFor(() => expect(client.agents).toHaveBeenCalledTimes(2));
        await act(async () => { finishRead({ agents: [{ ...agent, urgent: true }] }); });
      }
      else if (outcome === "rejected") failWrite(new HttpRequestError(400, "bad_request", "rejected", null));
      else failWrite(new Error("offline"));
    }
    if (outcome === "success") {
      await waitFor(() => expect(client.setAgentUrgency).toHaveBeenCalledOnce());
      expect(screen.getByRole("button", { name: "Clear urgency" }).hasAttribute("disabled")).toBe(false);
      remounted.rerender(<ConversationHeader agent={{ ...agent, urgent: true }} client={client} />);
      await waitFor(() => expect(screen.getByRole("button", { name: "Clear urgency" }).hasAttribute("disabled")).toBe(false));
    } else {
      const text = outcome === "read failure" ? "Could not check urgency" : outcome === "rejected" ? "Urgency update was rejected" : "Urgency update outcome is unknown";
      await waitFor(() => expect(screen.getByRole("alert").textContent).toContain(text));
      expect(screen.getByRole("button", { name: "Mark urgent" }).hasAttribute("disabled")).toBe(outcome === "uncertain");
    }
  });
});
