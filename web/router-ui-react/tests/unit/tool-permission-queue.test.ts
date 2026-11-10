import { describe, expect, it } from "vitest";
import { acknowledgePermissionError, awaitPermissionWrites, enqueuePermissionWrite, MAX_RETAINED_PERMISSION_FAILURES } from "../../src/components/tool-permission-queue";

describe("permission write retention", () => {
  it("retains failures until acknowledged and removes settled successes", async () => {
    const client = {};
    await enqueuePermissionWrite(client, "ok", async () => {});
    expect(await awaitPermissionWrites(client, "ok")).toBeUndefined();
    await expect(enqueuePermissionWrite(client, "bad", async () => { throw new Error("rejected"); })).rejects.toThrow("rejected");
    expect(await awaitPermissionWrites(client, "bad")).toContain("rejected");
    await enqueuePermissionWrite(client, "bad", async () => {});
    expect(await awaitPermissionWrites(client, "bad")).toContain("rejected");
    acknowledgePermissionError(client, "bad");
    expect(await awaitPermissionWrites(client, "bad")).toBeUndefined();
  });

  it("bounds retained failures and caps messages without evicting pending writes", async () => {
    const client = {};
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    const pending = enqueuePermissionWrite(client, "pending", async () => { await gate; });
    for (let i = 0; i <= MAX_RETAINED_PERMISSION_FAILURES; i++) {
      await expect(enqueuePermissionWrite(client, `key-${i}`, async () => { throw new Error("x".repeat(1000)); })).rejects.toThrow();
      await awaitPermissionWrites(client, `key-${i}`);
    }
    expect(await awaitPermissionWrites(client, "key-0")).toBeUndefined();
    expect((await awaitPermissionWrites(client, `key-${MAX_RETAINED_PERMISSION_FAILURES}`))?.length).toBe(512);
    let settled = false;
    const waiting = awaitPermissionWrites(client, "pending").then(() => { settled = true; });
    await Promise.resolve(); expect(settled).toBe(false);
    release(); await pending; await waiting; expect(settled).toBe(true);
  });

  it("waits for writes added while a previous write is pending", async () => {
    const client = {};
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    const first = enqueuePermissionWrite(client, "agent", async () => { await gate; });
    const waiting = awaitPermissionWrites(client, "agent");
    const second = enqueuePermissionWrite(client, "agent", async () => { throw new Error("second failed"); });
    const rejected = expect(second).rejects.toThrow("second failed");
    release(); await first; await rejected;
    expect(await waiting).toContain("second failed");
  });
});
