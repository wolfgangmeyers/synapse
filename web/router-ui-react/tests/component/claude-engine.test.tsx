import "@testing-library/jest-dom/vitest";
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within, waitFor } from "@testing-library/react";
import { AgentRow } from "../../src/components/AgentRow";
import { ModelPicker } from "../../src/components/ModelPicker";
import { Conversation } from "../../src/components/Conversation";
import { ManagedAgentDialog } from "../../src/components/ManagedAgentDialog";
import { ProviderAuthDialog } from "../../src/components/ProviderAuthDialog";
import type { ProviderModel } from "../../src/transport/http-client";
import { createHttpClient } from "../../src/transport/http-client";

const pi: ProviderModel = { provider: "google-vertex", id: "gemini", name: "Gemini", available: true };
const claude: ProviderModel = { provider: "claude-code", id: "sonnet", name: "Sonnet", available: true };
const opus = { ...claude, id: "opus", name: "Opus" };
const catalog = { auth_revision: 0, default_model: pi, models: [pi, claude, opus] };
afterEach(() => { cleanup(); localStorage.clear(); vi.restoreAllMocks(); });

// jsdom has no layout; give the portal and its containing dialog visible bounds.
function mockLayout() {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ x: 40, y: 40, left: 40, top: 40, right: 840, bottom: 640, width: 800, height: 600, toJSON: () => ({}) });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(600);
}

it("accepts Claude aliases through the existing model catalog without changing auth providers", async () => {
  const client = createHttpClient("http://127.0.0.1:8765", async () => new Response(JSON.stringify(catalog)));
  expect((await client.models()).models).toEqual(catalog.models);
});

it.each([claude, pi])("scopes embedded model choices to the current engine ($provider)", (currentModel) => {
  mockLayout();
  render(<ModelPicker isEmbedded models={catalog.models} currentModel={currentModel} onChange={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: `${currentModel.provider}/${currentModel.id}` }));
  expect(screen.getAllByRole("option")).toHaveLength(currentModel === pi ? 1 : 2);
  expect(screen.queryByRole("option", { name: currentModel === pi ? /Sonnet/ : /Gemini/ })).not.toBeInTheDocument();
});

it("launches Claude using the existing model key and keeps router default implicit", async () => {
  mockLayout();
  const onLaunch = vi.fn(async () => undefined);
  render(<ManagedAgentDialog open onClose={vi.fn()} onAction={vi.fn()} onLaunch={onLaunch} onListDirectories={async path => ({ path, parent: "/", directories: [] })} onModels={async () => catalog} error="Sign in with claude on the router host" />);
  await screen.findByRole("button", { name: "Use router default" });
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "worker" } });
  fireEvent.change(screen.getByLabelText(/Working folder/), { target: { value: "/workspace" } });
  fireEvent.click(screen.getByRole("button", { name: "Use router default" }));
  fireEvent.click(await screen.findByRole("option", { name: /Sonnet/ }));
  fireEvent.click(screen.getByRole("button", { name: "Launch" }));
  expect(onLaunch).toHaveBeenLastCalledWith({ name: "worker", cwd: "/workspace", runtime: "embedded", model: { provider: "claude-code", id: "sonnet" } });
  fireEvent.click(screen.getByRole("button", { name: "claude-code/sonnet" }));
  fireEvent.click(screen.getByRole("option", { name: /Use router default/ }));
  fireEvent.click(screen.getByRole("button", { name: "Launch" }));
  expect(onLaunch).toHaveBeenLastCalledWith({ name: "worker", cwd: "/workspace", runtime: "embedded" });
  expect(screen.getByRole("alert")).toHaveTextContent("Sign in with claude on the router host");
});

it("shows Claude router controls while hiding inline controls and retaining timeline", () => {
  localStorage.setItem("mecha.toolbar.pinnedControls.v2", JSON.stringify(["workflow", "inlineTools", "workerTools", "toolSettings", "goal"]));
  const thresholds = vi.fn();
  render(<Conversation peer={{ agent_id: "claude-worker", source: "router-managed", runtime: "embedded", current_model: claude, workflow_state: "Old state", context_usage: { tokens: 100, context_window: 1000, percent: 10 } }} messages={[{ id: "history", body: "Retained history" }]} onContextThresholds={thresholds} onGoal={vi.fn()} />);
  for (const name of [/Inline tools/, /Inline tool settings/, /Subagents:/]) expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  expect(screen.getByLabelText(/Workflow state:/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Set goal" })).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /Context usage:/ }));
  expect(thresholds).toHaveBeenCalledOnce();
  fireEvent.click(screen.getByRole("button", { name: "More agent controls" }));
  const menu = screen.getByRole("menu");
  for (const name of [/^Inline tools$/, /^Inline tool settings$/]) expect(within(menu).queryByRole("button", { name })).not.toBeInTheDocument();
  for (const name of [/^Workflow$/, /^Worker tools$/]) expect(within(menu).getByRole("button", { name })).toBeInTheDocument();
  expect(within(menu).getByRole("button", { name: "Reset" })).toBeInTheDocument();
  expect(screen.getByText("Retained history")).toBeInTheDocument();
});

it("offers Claude as the router default without an auth settings row", async () => {
  const client = createHttpClient("http://127.0.0.1:8765", async input => new Response(JSON.stringify(String(input).endsWith("/v1/models") ? catalog : { auth_revision: 0, providers: [
    ["openai-codex", "ChatGPT"], ["google", "Google"], ["google-vertex", "Google Vertex AI"], ["cursor", "Cursor"],
  ].map(([provider, label]) => ({ provider, label, authenticated: false, status: "signed_out", updated_at: "2030-01-01T00:00:00.000Z" })) })));
  render(<ProviderAuthDialog open client={client} onClose={vi.fn()} onPromptHandled={vi.fn()} />);
  expect(await screen.findByText(/Ordinary managed worker launches use this model when no model is chosen\. Embedded subagents may use the default or inherit their parent’s model\./)).toBeInTheDocument();
  const select = await screen.findByRole("combobox", { name: "Default model" });
  await waitFor(() => expect(within(select).getByRole("option", { name: /Gemini/ })).toBeInTheDocument());
  expect(within(select).getByRole("option", { name: /Sonnet/ })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: /Claude/i })).not.toBeInTheDocument();
});

it("suppresses Claude sidebar subagents while keeping Pi native subagents", () => {
  const agent = { agent_id: "worker", source: "router-managed" as const, runtime: "embedded", current_model: claude, subagent_count: 1, managed_children: [{ id: "child", parent_entry_id: "entry", task_id: "task", run_id: "run", sequence: 1, recovery: { state: "confirmed" as const, cause: null, observed_at: "2030-01-01T00:00:00Z" }, compaction_state: "none" as const, compaction: { state: "idle" as const, count: 0 }, description: "Native child", status: "running" as const }] };
  const { rerender } = render(<AgentRow agent={agent} />);
  expect(screen.queryByRole("button", { name: /subagent/i })).not.toBeInTheDocument();
  rerender(<AgentRow agent={{ ...agent, current_model: pi }} />);
  expect(screen.getByRole("button", { name: /subagent/i })).toBeInTheDocument();
});

it("rejects a Claude provider-auth row instead of widening the four-provider contract", async () => {
  const client = createHttpClient("http://127.0.0.1:8765", async () => new Response(JSON.stringify({ auth_revision: 0, providers: [{ provider: "claude-code", label: "Claude", authenticated: true, status: "authenticated", updated_at: "2030-01-01T00:00:00.000Z" }] })));
  await expect(client.providerAuth()).rejects.toThrow("invalid provider state");
});
