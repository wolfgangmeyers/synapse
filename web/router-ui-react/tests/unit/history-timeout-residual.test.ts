import { describe, expect, it, vi } from "vitest";
import { createHttpClient } from "../../src/transport/http-client";

describe("history deadline", () => {
  it("aborts stalled timeline and managed history and rejects", async () => {
    vi.useFakeTimers();
    try {
      const signals: AbortSignal[] = [];
      const fetcher: typeof fetch = (_input, init) => {
        if (init?.signal) signals.push(init.signal);
        return new Promise(() => {});
      };
      const client = createHttpClient("http://localhost:8765", fetcher);
      const a = client.fetchConversationTimeline("peer");
      const b = client.managedHistory("peer", "complete");
      const checks = Promise.all([expect(a).rejects.toThrow(/timed out/i), expect(b).rejects.toThrow(/timed out/i)]);
      await vi.advanceTimersByTimeAsync(15000);
      await checks;
      expect(signals).toHaveLength(2);
      expect(signals.every((signal) => signal.aborted)).toBe(true);
    } finally { vi.useRealTimers(); }
  });
});
