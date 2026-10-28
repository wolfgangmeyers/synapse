import { describe, expect, it } from "vitest";
import { mergeMessages, pruneHistoryMessages, reconcileHistoryMessages, settlePendingMessages, stripDataUrls } from "../../src/domain/history";
const message = (id: string, sequence: number, incarnation = "") => ({ id, sequence, message_incarnation_id: incarnation, created_at: "2026-01-01T00:00:00.000Z", body: id });
describe("history", () => {
  it("keeps incarnation identities and sequence order", () => { expect(mergeMessages([message("b", 2)], [message("a", 1), message("b", 3, "new")]).map((item) => item.id)).toEqual(["a", "b", "b"]); });
  it("retains live messages beyond snapshot high water", () => { expect(reconcileHistoryMessages([message("live", 9), message("old", 1)], [message("snap", 2)], 2).map((item) => item.id)).toEqual(["snap", "live"]); });
  it("replaces an optimistic pending row when canonical history has a different id", () => {
    const createdAt = "2026-01-01T00:00:00.000Z";
    const pending = { id: "local-id", from: "web", to: "peer", body: "hello", created_at: createdAt, direction: "outgoing" as const, pending: true };
    const canonical = { id: "router-id", sequence: 3, from: "web", to: "peer", body: "hello", created_at: createdAt, recorded_at: createdAt };
    const result = reconcileHistoryMessages([pending], [canonical], 3);
    expect(result).toEqual([canonical]);
    expect(reconcileHistoryMessages([canonical], [{ ...pending }], 0)).toEqual([canonical]);
  });
  it("drops every pending duplicate for a canonical message ID", () => {
    const pending = { id: "message-1", from: "web", to: "peer", body: "hello", created_at: "2026-01-01T00:00:00.000Z", direction: "outgoing" as const, pending: true };
    const staleDuplicate = { ...pending, message_incarnation_id: "123e4567-e89b-42d3-a456-426614174000", created_at: "2026-01-01T00:00:01.000Z" };
    const canonical = { id: "message-1", message_incarnation_id: "123e4567-e89b-42d3-a456-426614174001", sequence: 3, from: "web", to: "peer", body: "hello", created_at: "2026-01-01T00:00:02.000Z", recorded_at: "2026-01-01T00:00:02.000Z" };
    expect(reconcileHistoryMessages([pending, staleDuplicate], [canonical], 3)).toEqual([canonical]);
  });
  it("settles a failed optimistic message so persisted reload state is not pending", () => {
    const pending = { id: "local-id", from: "web", to: "peer", body: "hello", pending: true, direction: "outgoing" as const };
    expect(settlePendingMessages([pending], "local-id", false)).toEqual([{ ...pending, pending: false, failed: true }]);
  });
  it("bounds age and count and strips data URLs", () => { const now = Date.parse("2026-02-01T00:00:00.000Z"); const many = Array.from({ length: 5001 }, (_, i) => ({ ...message(String(i), i), created_at: new Date(now).toISOString() })); expect(pruneHistoryMessages(many, now)).toHaveLength(5000); expect(stripDataUrls({ ...message("x", 1), attachments: [{ url: "data:image/jpeg;base64,x" }, { url: "/v1/attachments/x.jpg" }] }).attachments).toHaveLength(1); });
});
