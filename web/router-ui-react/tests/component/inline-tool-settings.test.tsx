import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import { InlineToolSettingsDialog } from "../../src/components/InlineToolSettingsDialog";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import { isRecord } from "../../src/protocol/guards";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";

beforeEach(() => { localStorage.clear(); });
afterEach(() => { cleanup(); localStorage.clear(); });

function jsonResponse(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
}

class InlineToolSocket implements WebSocketLike {
  readonly sent: unknown[] = [];
  readyState = 0;
  private sourceAgentId = "web-ui";
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();

  constructor(readonly mode: "reject" | "disconnect", readonly onControl: () => void) {}

  addEventListener(type: string, listener: (event: SocketEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  send(value: string): void {
    const frame: unknown = JSON.parse(value);
    this.sent.push(frame);
    if (!isRecord(frame)) return;
    if (frame.type === "register" && typeof frame.frame_id === "string" && typeof frame.agent_id === "string" && typeof frame.instance_id === "string") {
      this.sourceAgentId = frame.agent_id;
      this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: frame.frame_id, agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: "web-lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
      return;
    }
    if (frame.type !== "inline_tool_control" || typeof frame.control_id !== "string") return;
    this.onControl();
    if (this.mode === "reject") {
      this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "inline-result", type: "inline_tool_control_result", agent_id: "worker-one", instance_id: "peer-instance", lease_id: "peer-lease", source_agent_id: this.sourceAgentId, control_id: frame.control_id, accepted: false, error_code: "permission_denied", enabled: false }) });
    }
  }

  close(): void {
    this.readyState = 3;
    this.emit("close", {});
  }

  emit(type: string, event: SocketEvent): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

const inlinePeer = { agent_id: "worker-one", instance_id: "peer-instance", agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state: "idle" as const, subagent_count: 0, subagents: [], available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false, inline_tool_control_available: true, inline_tool_enabled: false };

function renderInlineToolApp(mode: "reject" | "disconnect", inlineCapability = true): { fetchImpl: ReturnType<typeof vi.fn>; socket: InlineToolSocket } {
  let targetPresent = true;
  const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/v1/agents")) return jsonResponse({ agents: targetPresent ? [inlinePeer] : [] });
    if (url.endsWith("/v1/capabilities")) return jsonResponse({ inline_tool_control_v1: inlineCapability });
    if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "worker-one", high_water_sequence: 0, messages: [] });
    if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
    if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
    throw new Error(`Unexpected request: ${url}`);
  });
  const onControl = (): void => {
    if (mode !== "disconnect") return;
    targetPresent = false;
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "inline-state", type: "inline_tool_state", agent_id: "worker-one", instance_id: "peer-instance", lease_id: "peer-lease", enabled: true }) });
  };
  const socket = new InlineToolSocket(mode, onControl);
  const webSocketFactory = (): WebSocketLike => {
    queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
    return socket;
  };
  const storage = new MemoryStorage();
  render(<App routerUrl="http://inline-tools.test:43123" agentId="web-ui" storage={storage} transportOptions={{ fetchImpl, webSocketFactory, storage }} />);
  return { fetchImpl, socket };
}

describe("inline tool settings", () => {
  it("opens from an accessible header gear and saves the selected value", async () => {
    const user = userEvent.setup();
    const onOpen = vi.fn();
    const onSave = vi.fn();
    render(<ConversationHeader agent={{ agent_id: "worker-one", activity_state: "idle" }} onInlineToolSettings={onOpen} />);
    await user.click(screen.getByRole("button", { name: "Inline tool settings" }));
    expect(onOpen).toHaveBeenCalledOnce();
    render(<InlineToolSettingsDialog open peer="worker-one" enabled={false} capabilityAvailable onClose={vi.fn()} onSave={onSave} />);
    await user.click(screen.getByRole("button", { name: "Allow inline tools: OFF" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(onSave).toHaveBeenCalledWith(true);
    expect(screen.getByText(/This allows top-level bash, edit, and write/)).toBeInTheDocument();
  });

  it("explains unsupported workers and disables the setting", () => {
    render(<InlineToolSettingsDialog open peer="worker-one" enabled={true} capabilityAvailable={false} onClose={vi.fn()} onSave={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Allow inline tools: ON" })).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent("not available");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("shows pending save state and keeps cancel available", async () => {
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<InlineToolSettingsDialog open peer="worker-one" enabled={false} capabilityAvailable pending onClose={onClose} onSave={vi.fn()} />);
    const toggle = screen.getByRole("button", { name: "Allow inline tools: OFF" });
    expect(toggle).toBeEnabled();
    expect(toggle).toHaveClass("goal-toggle");
    expect(screen.getByRole("button", { name: "Saving…" })).toBeDisabled();
    await user.click(toggle);
    expect(screen.getByRole("button", { name: "Allow inline tools: ON" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  });

  it("calls the close handler for cancel and the close button", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<InlineToolSettingsDialog open peer="worker-one" enabled={false} capabilityAvailable onClose={onClose} onSave={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(screen.getByRole("button", { name: "Close inline tool settings" }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("restores focus to the opening control when closed", async () => {
    const returnFocus = document.createElement("button");
    document.body.append(returnFocus);
    const { rerender } = render(<InlineToolSettingsDialog open peer="worker-one" enabled={false} capabilityAvailable returnFocus={returnFocus} onClose={vi.fn()} onSave={vi.fn()} />);
    rerender(<InlineToolSettingsDialog open={false} peer="worker-one" enabled={false} capabilityAvailable returnFocus={returnFocus} onClose={vi.fn()} onSave={vi.fn()} />);
    await waitFor(() => expect(returnFocus).toHaveFocus());
    returnFocus.remove();
  });

  it("shows the unavailable capability in the connected application", async () => {
    const user = userEvent.setup();
    renderInlineToolApp("reject", false);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    await user.click(screen.getByRole("button", { name: "Inline tool settings" }));
    expect(await screen.findByRole("status")).toHaveTextContent("not available");
    expect(screen.getByRole("button", { name: "Allow inline tools: OFF" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps the dialog open when the worker rejects a setting", async () => {
    const user = userEvent.setup();
    const { socket } = renderInlineToolApp("reject");
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    await user.click(screen.getByRole("button", { name: "Inline tool settings" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Allow inline tools: OFF" })).not.toBeDisabled());
    await user.click(screen.getByRole("button", { name: "Allow inline tools: OFF" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("permission_denied"));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(socket.sent.some((frame) => isRecord(frame) && frame.type === "inline_tool_control")).toBe(true);
  });

  it("removes a pending save when the target disconnects", async () => {
    const user = userEvent.setup();
    const { socket } = renderInlineToolApp("disconnect");
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    await user.click(screen.getByRole("button", { name: "Inline tool settings" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Allow inline tools: OFF" })).not.toBeDisabled());
    await user.click(screen.getByRole("button", { name: "Allow inline tools: OFF" }));
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Worker One (worker-one)" })).not.toBeInTheDocument());
    expect(socket.sent.some((frame) => isRecord(frame) && frame.type === "inline_tool_control")).toBe(true);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
