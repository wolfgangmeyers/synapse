import { describe, expect, it } from "vitest";
import { avatarForAgent, selectRendezvousAvatarKey } from "../../src/components/AgentAvatar";

describe("agent avatars", () => {
  it("maps an agent name deterministically to a bundled image and color", () => {
    const expected = { src: "/avatars/robot111.png", color: "#e05252" };

    expect(avatarForAgent("build agent")).toEqual(expected);
    expect(avatarForAgent("build agent")).toEqual(expected);
    expect(expected.src).not.toMatch(/^https?:/);
    expect(avatarForAgent("fixture peer")).toEqual({ src: "/avatars/robot070.png", color: "#74b7e7" });
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

  it("keeps all 36 softened bright palette entries and their hash mapping stable", () => {
    const fixtureAgentNames = [
      "palette-fixture-194",
      "palette-fixture-5",
      "palette-fixture-4",
      "palette-fixture-7",
      "palette-fixture-26",
      "palette-fixture-9",
      "palette-fixture-37",
      "palette-fixture-12",
      "palette-fixture-107",
      "palette-fixture-47",
      "palette-fixture-0",
      "palette-fixture-106",
      "palette-fixture-62",
      "palette-fixture-32",
      "palette-fixture-19",
      "palette-fixture-23",
      "palette-fixture-22",
      "palette-fixture-18",
      "palette-fixture-68",
      "palette-fixture-3",
      "palette-fixture-2",
      "palette-fixture-1",
      "palette-fixture-20",
      "palette-fixture-16",
      "palette-fixture-17",
      "palette-fixture-14",
      "palette-fixture-42",
      "palette-fixture-34",
      "palette-fixture-6",
      "palette-fixture-43",
      "palette-fixture-86",
      "palette-fixture-67",
      "palette-fixture-44",
      "palette-fixture-54",
      "palette-fixture-28",
      "palette-fixture-74",
    ];
    const expectedColors = [
      "#e56991",
      "#e18156",
      "#e2c35c",
      "#92cc5d",
      "#47c2ac",
      "#53bae1",
      "#8699ea",
      "#ae85ea",
      "#db7ae7",
      "#e05252",
      "#e78b79",
      "#e6ab70",
      "#dfad49",
      "#e7de79",
      "#c7e04d",
      "#c0e774",
      "#93e25a",
      "#91e774",
      "#56e156",
      "#67e487",
      "#52e099",
      "#70e6be",
      "#56e1d6",
      "#67e4e4",
      "#52c9e0",
      "#74b7e7",
      "#5a93e2",
      "#6c8ae5",
      "#5656e1",
      "#8470e6",
      "#a552e0",
      "#c76ce5",
      "#e156d6",
      "#e774ca",
      "#e25aa9",
      "#e87d8f",
    ];

    expect(expectedColors).toHaveLength(36);
    expect(new Set(expectedColors).size).toBe(36);
    expect(fixtureAgentNames.map((agentName) => avatarForAgent(agentName).color)).toEqual(expectedColors);
  });
});
