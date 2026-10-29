import { describe, expect, it } from "vitest";
import {
  DEFAULT_AUTO_SCROLL_THRESHOLD,
  shouldFollowToBottom,
} from "../../src/state/conversation-projection";

describe("shouldFollowToBottom", () => {
  it("exports DEFAULT_AUTO_SCROLL_THRESHOLD as 180", () => {
    expect(DEFAULT_AUTO_SCROLL_THRESHOLD).toBe(180);
  });

  describe("with metrics object", () => {
    it("returns true when exactly at the threshold (180px from bottom)", () => {
      // scrollHeight: 1000, clientHeight: 500, scrollTop: 320 -> distance = 1000 - (320 + 500) = 180
      const metrics = { scrollTop: 320, scrollHeight: 1000, clientHeight: 500 };
      expect(shouldFollowToBottom(metrics)).toBe(true);
    });

    it("returns false when just beyond the threshold (181px from bottom)", () => {
      // scrollHeight: 1000, clientHeight: 500, scrollTop: 319 -> distance = 1000 - (319 + 500) = 181
      const metrics = { scrollTop: 319, scrollHeight: 1000, clientHeight: 500 };
      expect(shouldFollowToBottom(metrics)).toBe(false);
    });

    it("returns true when at the bottom (0px distance)", () => {
      const metrics = { scrollTop: 500, scrollHeight: 1000, clientHeight: 500 };
      expect(shouldFollowToBottom(metrics)).toBe(true);
    });

    it("returns true when overscrolled past bottom (negative distance)", () => {
      const metrics = { scrollTop: 520, scrollHeight: 1000, clientHeight: 500 };
      expect(shouldFollowToBottom(metrics)).toBe(true);
    });

    it("returns true for empty container (scrollHeight === clientHeight === 0)", () => {
      const metrics = { scrollTop: 0, scrollHeight: 0, clientHeight: 0 };
      expect(shouldFollowToBottom(metrics)).toBe(true);
    });

    it("respects custom threshold parameter when provided", () => {
      const metrics = { scrollTop: 300, scrollHeight: 1000, clientHeight: 500 }; // distance = 200
      expect(shouldFollowToBottom(metrics, 250)).toBe(true);
      expect(shouldFollowToBottom(metrics, 150)).toBe(false);
    });
  });

  describe("with individual scalar arguments (legacy overload)", () => {
    it("defaults to 180 threshold when threshold argument is omitted", () => {
      // distance = 1000 - (320 + 500) = 180
      expect(shouldFollowToBottom(320, 500, 1000)).toBe(true);
      // distance = 1000 - (319 + 500) = 181
      expect(shouldFollowToBottom(319, 500, 1000)).toBe(false);
    });

    it("returns true when distance is 0 or negative", () => {
      expect(shouldFollowToBottom(500, 500, 1000)).toBe(true);
      expect(shouldFollowToBottom(520, 500, 1000)).toBe(true);
    });

    it("respects custom threshold", () => {
      expect(shouldFollowToBottom(300, 500, 1000, 200)).toBe(true);
      expect(shouldFollowToBottom(300, 500, 1000, 199)).toBe(false);
    });
  });
});
