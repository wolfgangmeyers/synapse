import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { AgentRow } from "../../src/components/AgentRow";
import type { Agent } from "../../src/state/agent-projection";

afterEach(cleanup);

it.each([
  [{ agent_id: "claude", source: "router-managed", current_model: { provider: "claude-code", id: "sonnet" } }, "claude"],
  [{ agent_id: "pi", source: "router-managed", current_model: { provider: "anthropic", id: "sonnet" } }, null],
  [{ agent_id: "missing", source: "router-managed" }, null],
  [{ agent_id: "terminal", source: "tmux", current_model: { provider: "claude-code", id: "sonnet" } }, "tmux"],
  [{ agent_id: "legacy" }, "tmux"],
] satisfies [Agent, string | null][])('renders the right source tag for $agent_id', (agent, label) => {
  const { container } = render(<AgentRow agent={agent} />);
  const tags = container.querySelectorAll(".agent-name-line .agent-source");
  expect(tags).toHaveLength(label ? 1 : 0);
  if (label) expect(tags[0]).toHaveTextContent(label);
});
