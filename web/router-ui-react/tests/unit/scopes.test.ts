import { describe, expect, it } from "vitest";
import { draftScope, matchesActiveScope, matchesConnectionScope, ownerScope, peerScope } from "../../src/state/scopes";
describe("scopes", () => {
  it("builds stable owner, peer, and draft scopes", () => { expect(ownerScope("http://r", "web")).toBe("http://r\u001fweb"); expect(peerScope("http://r", "web", "peer")).toBe("http://r\u001fweb\u001fpeer"); expect(draftScope("http://r", "web")).toBe(""); });
  it("rejects stale scoped events and accepts unscoped events", () => { const active = { routerUrl: "http://r", agentId: "web" }; expect(matchesActiveScope({ type: "message", routerUrl: "http://old", agentId: "web" }, active)).toBe(false); expect(matchesActiveScope({ type: "pong", routerUrl: "http://old", agentId: "web" }, active)).toBe(true); });
  it("guards connection generation and lease", () => { const active = { routerUrl: "r", agentId: "a", connectionGeneration: 2, instanceId: "i", leaseId: "l" }; expect(matchesConnectionScope({ type: "message", routerUrl: "r", agentId: "a", connectionGeneration: 1, instanceId: "i", leaseId: "l" }, active)).toBe(false); expect(matchesConnectionScope({ type: "message", routerUrl: "r", agentId: "a", connectionGeneration: 2, instanceId: "i", leaseId: "l" }, active)).toBe(true); });
});
