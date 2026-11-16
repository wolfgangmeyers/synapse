import "@testing-library/jest-dom/vitest";
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { isAntigravityModel, sameModelEngine } from "../../src/domain/models";
import { isAntigravityAgent } from "../../src/state/agent-projection";
import { AgentRow } from "../../src/components/AgentRow";
import type { ManagedAgent } from "../../src/protocol/types";

describe("antigravity models and agent projections", () => {
  it("identifies antigravity models correctly", () => {
    expect(isAntigravityModel("antigravity")).toBe(true);
    expect(isAntigravityModel("antigravity/claude-3-7-sonnet")).toBe(true);
    expect(isAntigravityModel("claude-code/sonnet")).toBe(false);
    expect(isAntigravityModel(null)).toBe(false);
    expect(isAntigravityModel(undefined)).toBe(false);
  });

  it("handles sameModelEngine for antigravity", () => {
    expect(sameModelEngine("antigravity/model-a", "antigravity/model-b")).toBe(true);
    expect(sameModelEngine("antigravity/model-a", "openai/gpt-4o")).toBe(false);
    expect(sameModelEngine("antigravity", "antigravity/foo")).toBe(true);
    expect(sameModelEngine({ provider: "antigravity", id: "gemini-2.5" }, { provider: "antigravity", id: "gemini-flash" })).toBe(true);
    expect(sameModelEngine({ provider: "antigravity", id: "gemini-2.5" }, { provider: "openai", id: "gpt-4o" })).toBe(false);
  });

  it("identifies antigravity agents", () => {
    expect(isAntigravityAgent(null)).toBe(false);
    expect(isAntigravityAgent(undefined)).toBe(false);

    const agentWithModelId = {
      model: { id: "antigravity/gemini-2.5-flash", provider: "google" },
    } as unknown as ManagedAgent;
    expect(isAntigravityAgent(agentWithModelId)).toBe(true);

    const agentWithProvider = {
      model: { id: "custom-model", provider: "antigravity" },
    } as unknown as ManagedAgent;
    expect(isAntigravityAgent(agentWithProvider)).toBe(true);

    const normalAgent = {
      model: { id: "claude-code/sonnet", provider: "anthropic" },
    } as unknown as ManagedAgent;
    expect(isAntigravityAgent(normalAgent)).toBe(false);
  });

  it("renders antigravity worker badge on AgentRow", () => {
    const { container } = render(
      <AgentRow
        agent={{
          agent_id: "agy-test",
          source: "router-managed",
          current_model: { provider: "antigravity", id: "gemini-2.5-pro" },
          managed_agent: {
            agent_id: "agy-test",
            runtime: "embedded",
            model: { provider: "antigravity", id: "gemini-2.5-pro" },
          } as unknown as ManagedAgent,
        }}
      />
    );
    const tags = container.querySelectorAll(".agent-name-line .agent-source");
    expect(tags).toHaveLength(1);
    expect(tags[0]).toHaveTextContent("antigravity");
  });
});

