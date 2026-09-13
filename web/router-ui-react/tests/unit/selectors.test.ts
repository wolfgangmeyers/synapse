import { describe, expect, it } from "vitest";
import { humanName } from "../../src/state/selectors";

describe("humanName", () => {
  it.each([
    ["my-AGENT", "My AGENT"],
    ["MY-AGENT", "MY AGENT"],
  ])("preserves segment casing for %s", (id, displayName) => {
    expect(humanName(id)).toBe(displayName);
  });
});
