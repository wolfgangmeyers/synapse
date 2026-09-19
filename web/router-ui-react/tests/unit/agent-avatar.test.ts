import { describe, expect, it } from "vitest";
import { avatarForAgent, selectRendezvousAvatarKey } from "../../src/components/AgentAvatar";

describe("agent avatars", () => {
  it("maps an agent name deterministically to a bundled image and color", () => {
    const expected = { src: "/avatars/robot019.png", color: "#e56991" };

    expect(avatarForAgent("build agent")).toEqual(expected);
    expect(avatarForAgent("build agent")).toEqual(expected);
    expect(expected.src).not.toMatch(/^https?:/);
    expect(avatarForAgent("fixture peer")).toEqual({ src: "/avatars/robot030.png", color: "#ae85ea" });
  });

  it("selects the same rendezvous winner regardless of candidate order", () => {
    const candidates = ["robot-a.png", "robot-b.png", "robot-c.png"];

    expect(selectRendezvousAvatarKey("agent-3", candidates)).toBe("robot-c.png");
    expect(selectRendezvousAvatarKey("agent-3", [...candidates].reverse())).toBe("robot-c.png");
    expect(selectRendezvousAvatarKey("agent-3", [candidates[1]!, candidates[2]!, candidates[0]!])).toBe("robot-c.png");
  });

  it("keeps the previous rendezvous winner unless the added candidate wins", () => {
    const candidates = ["robot-a.png", "robot-b.png", "robot-c.png"];

    expect(selectRendezvousAvatarKey("agent-0", candidates)).toBe("robot-b.png");
    expect(selectRendezvousAvatarKey("agent-0", [...candidates, "robot-d.png"])).toBe("robot-b.png");
    expect(selectRendezvousAvatarKey("agent-3", candidates)).toBe("robot-c.png");
    expect(selectRendezvousAvatarKey("agent-3", [...candidates, "robot-g.png"])).toBe("robot-g.png");
  });

  it("keeps every deterministic palette entry at 70% of its original saturation", () => {
    expect([
      avatarForAgent("agent-2").color,
      avatarForAgent("agent-21").color,
      avatarForAgent("agent-1").color,
      avatarForAgent("agent-0").color,
      avatarForAgent("agent-9").color,
      avatarForAgent("agent-8").color,
      avatarForAgent("agent-5").color,
      avatarForAgent("agent-3").color,
      avatarForAgent("agent-7").color,
    ]).toEqual([
      "#e56991",
      "#e18156",
      "#e2c35c",
      "#92cc5d",
      "#47c2ac",
      "#53bae1",
      "#8699ea",
      "#ae85ea",
      "#db7ae7",
    ]);
  });
});
