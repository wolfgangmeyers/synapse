import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, act } from "@testing-library/react";
import { useManagedAgents } from "../../src/hooks/use-managed-agents";
import type { RouterHttpClient } from "../../src/transport/http-client";
import type { ManagedAgent } from "../../src/protocol/types";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const sampleAgent: ManagedAgent = {
  agent_id: "agent-alpha",
  source: "router-managed",
  cwd: "/workspace",
  model: { provider: "antigravity", id: "gemini-3.8-flash" },
  desired_state: "running",
  lifecycle_state: "running",
  activity_state: "active",
  accepting_prompts: true,
  manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 5 },
  created_at: "2026-10-01T00:00:00.000Z",
  started_at: "2026-10-01T00:00:00.000Z",
  updated_at: "2026-10-01T00:00:00.000Z",
  last_error: null,
};

describe("useManagedAgents hook", () => {
  it("periodically polls managed agents and notifies onReconcile", async () => {
    let callCount = 0;
    const client = {
      managedAgents: vi.fn(async () => {
        callCount++;
        return {
          generated_at: new Date().toISOString(),
          projection_version: callCount,
          agents: [
            {
              ...sampleAgent,
              activity_state: callCount === 1 ? ("active" as const) : ("idle" as const),
            },
          ],
        };
      }),
    } as unknown as RouterHttpClient;

    const onReconcile = vi.fn();
    const { result } = renderHook(() =>
      useManagedAgents(client, { enabled: false, onReconcile })
    );

    expect(result.current.ready).toBe(false);

    // Initial manual refresh
    await act(async () => {
      await result.current.refresh();
    });

    expect(result.current.ready).toBe(true);
    expect(result.current.agents).toHaveLength(1);
    expect(result.current.agents[0]?.activity_state).toBe("active");
    expect(onReconcile).toHaveBeenCalledTimes(1);

    // Second manual refresh
    await act(async () => {
      await result.current.refresh();
    });

    expect(result.current.agents[0]?.activity_state).toBe("idle");
    expect(onReconcile).toHaveBeenCalledTimes(2);
  });

  it("handles fetch errors gracefully without breaking the hook", async () => {
    const client = {
      managedAgents: vi.fn(async () => {
        throw new Error("Network offline");
      }),
    } as unknown as RouterHttpClient;

    const { result } = renderHook(() =>
      useManagedAgents(client, { enabled: false })
    );

    await act(async () => {
      await result.current.refresh();
    });

    expect(result.current.error).toBeDefined();
    expect(result.current.error?.message).toBe("Network offline");
    expect(result.current.agents).toEqual([]);
    expect(result.current.ready).toBe(false);
  });
});
