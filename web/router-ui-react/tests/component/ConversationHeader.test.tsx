import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import type { Agent } from "../../src/state/agent-projection";
import {
  formatCompactTokens,
  formatContextUsageLabel,
  formatContextUsageTooltip,
  contextUsageSeverityClass,
} from "../../src/utils/format";

describe("format utilities for context usage", () => {
  it("formats compact tokens correctly", () => {
    expect(formatCompactTokens(1_048_576)).toBe("1M");
    expect(formatCompactTokens(2_000_000)).toBe("2M");
    expect(formatCompactTokens(1_500_000)).toBe("1.5M");
    expect(formatCompactTokens(200_000)).toBe("200k");
    expect(formatCompactTokens(128_000)).toBe("128k");
    expect(formatCompactTokens(32_768)).toBe("33k");
    expect(formatCompactTokens(500)).toBe("500");
    expect(formatCompactTokens(0)).toBe("0");
  });

  it("formats context usage label correctly", () => {
    expect(formatContextUsageLabel({ tokens: 440120, context_window: 1048576, percent: 42.0 })).toBe("42% of 1M");
    expect(formatContextUsageLabel({ tokens: null, context_window: 200000, percent: null })).toBe("- / 200k");
  });

  it("formats context usage tooltip correctly", () => {
    expect(formatContextUsageTooltip({ tokens: 440120, context_window: 1048576, percent: 42.0 })).toBe("440,120 / 1,048,576 tokens (42.0%)");
    expect(formatContextUsageTooltip({ tokens: null, context_window: 100000, percent: null })).toBe("Unknown / 100,000 tokens (N/A)");
  });

  it("calculates severity class correctly", () => {
    expect(contextUsageSeverityClass(null)).toBe("normal");
    expect(contextUsageSeverityClass(undefined)).toBe("normal");
    expect(contextUsageSeverityClass(42)).toBe("normal");
    expect(contextUsageSeverityClass(49.9)).toBe("normal");
    expect(contextUsageSeverityClass(50)).toBe("warning");
    expect(contextUsageSeverityClass(79.9)).toBe("warning");
    expect(contextUsageSeverityClass(80)).toBe("critical");
    expect(contextUsageSeverityClass(95)).toBe("critical");
  });
});

describe("ConversationHeader context usage bar", () => {
  const baseAgent: Agent = {
    agent_id: "test-agent",
    source: "router-managed",
    activity_state: "idle",
  };

  it("renders context usage bar with normal severity (<50%)", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 440120,
        context_window: 1048576,
        percent: 42.0,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-context-usage");
    expect(bar).not.toBeNull();
    expect(bar?.getAttribute("title")).toBe("440,120 / 1,048,576 tokens (42.0%)");
    expect(bar?.getAttribute("aria-label")).toBe("Context usage: 42% of 1M");

    const fill = container.querySelector(".context-usage-fill");
    expect(fill).not.toBeNull();
    expect(fill?.classList.contains("normal")).toBe(true);
    expect(fill?.getAttribute("style")).toContain("width: 42%");

    const label = container.querySelector(".context-usage-label");
    expect(label?.textContent).toBe("42% of 1M");
  });

  it("renders context usage bar with warning severity (50-79%)", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 130000,
        context_window: 200000,
        percent: 65.0,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-context-usage");
    expect(bar).not.toBeNull();
    expect(bar?.getAttribute("title")).toBe("130,000 / 200,000 tokens (65.0%)");
    expect(bar?.getAttribute("aria-label")).toBe("Context usage: 65% of 200k");

    const fill = container.querySelector(".context-usage-fill");
    expect(fill).not.toBeNull();
    expect(fill?.classList.contains("warning")).toBe(true);
    expect(fill?.getAttribute("style")).toContain("width: 65%");

    const label = container.querySelector(".context-usage-label");
    expect(label?.textContent).toBe("65% of 200k");
  });

  it("renders context usage bar with critical severity (>=80%)", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 108800,
        context_window: 128000,
        percent: 85.0,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-context-usage");
    expect(bar).not.toBeNull();
    expect(bar?.getAttribute("title")).toBe("108,800 / 128,000 tokens (85.0%)");

    const fill = container.querySelector(".context-usage-fill");
    expect(fill).not.toBeNull();
    expect(fill?.classList.contains("critical")).toBe(true);
    expect(fill?.getAttribute("style")).toContain("width: 85%");

    const label = container.querySelector(".context-usage-label");
    expect(label?.textContent).toBe("85% of 128k");
  });

  it("omits context usage bar when agent has no context_usage (e.g. tmux worker)", () => {
    const agent: Agent = {
      agent_id: "tmux-worker",
      source: "tmux",
      activity_state: "idle",
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-context-usage");
    expect(bar).toBeNull();
  });

  it("omits context usage bar when context_window <= 0", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 0,
        context_window: 0,
        percent: null,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-context-usage");
    expect(bar).toBeNull();
  });
});
