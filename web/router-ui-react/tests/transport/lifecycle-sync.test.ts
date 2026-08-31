import { describe, expect, it } from "vitest";
import { createLifecycleTracker } from "../../src/transport/router-transport";

describe("lifecycle synchronization", () => {
  it("accepts monotonic snapshots, updates, and replay only once", () => {
    const tracker = createLifecycleTracker();
    expect(tracker.accept(4, "snapshot")).toBe(true); expect(tracker.accept(3, "update")).toBe(false); expect(tracker.accept(4, "replay")).toBe(true); expect(tracker.accept(4, "replay")).toBe(false); expect(tracker.version).toBe(4);
  });
  it("does not infer resync from a version gap", () => {
    const tracker = createLifecycleTracker(2); expect(tracker.shouldResync()).toBe(false); expect(tracker.shouldResync("recovery")).toBe(true);
  });
});
