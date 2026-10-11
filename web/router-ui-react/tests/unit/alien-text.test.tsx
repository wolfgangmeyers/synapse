import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import {
  actionCardColors,
  alienSymbolAssets,
  colorForAction,
  generateAlienText,
  generateAlienTokens,
  hashAlienInput,
} from "../../src/utils/alienText";
import { AlienText } from "../../src/components/AlienText";

describe("alien text generation system", () => {
  it("loads 64 distinct alien symbol assets", () => {
    expect(alienSymbolAssets).toHaveLength(64);
    expect(alienSymbolAssets[0]?.id).toBe("symbol001");
    expect(alienSymbolAssets[63]?.id).toBe("symbol064");
    // All IDs should be unique
    const ids = new Set(alienSymbolAssets.map((a) => a.id));
    expect(ids.size).toBe(64);
  });

  it("hashes inputs deterministically using FNV-1a", () => {
    const hash1 = hashAlienInput("bash");
    const hash2 = hashAlienInput("bash");
    expect(hash1).toBe(hash2);
    expect(typeof hash1).toBe("number");
    expect(hash1).toBeGreaterThan(0);
    // Number inputs should also hash
    expect(hashAlienInput(123)).toBe(hashAlienInput("123"));
  });

  it("deterministically generates symbols within minLength and maxLength (defaults to 3-7)", () => {
    const symbols = generateAlienText("action-id-42");
    expect(symbols.length).toBeGreaterThanOrEqual(3);
    expect(symbols.length).toBeLessThanOrEqual(7);

    // Repeated call with same seed yields identical symbol sequence
    const symbolsAgain = generateAlienText("action-id-42");
    expect(symbolsAgain).toEqual(symbols);

    // Explicit bounds work
    const bounded = generateAlienText("action-id-99", 3, 5);
    expect(bounded.length).toBeGreaterThanOrEqual(3);
    expect(bounded.length).toBeLessThanOrEqual(5);
  });

  it("handles minLength == maxLength", () => {
    const symbols = generateAlienText("fixed-length", 5, 5);
    expect(symbols).toHaveLength(5);
  });

  it("deterministically inserts space tokens for word breaks when symbol count >= 4", () => {
    // Check multiple inputs to ensure word spacing is inserted
    let foundSpace = false;
    for (let i = 0; i < 20; i += 1) {
      const tokens = generateAlienTokens(`seed-${i}`, 4, 7);
      const spaces = tokens.filter((t) => t.type === "space");
      const syms = tokens.filter((t) => t.type === "symbol");
      expect(syms.length).toBeGreaterThanOrEqual(4);
      expect(syms.length).toBeLessThanOrEqual(7);
      if (spaces.length > 0) {
        foundSpace = true;
        // Verify spaces are not at the edges
        expect(tokens[0]?.type).toBe("symbol");
        expect(tokens[tokens.length - 1]?.type).toBe("symbol");
      }
    }
    expect(foundSpace).toBe(true);
  });

  it("provides at least 32 distinct action colors in the pool", () => {
    expect(actionCardColors.length).toBeGreaterThanOrEqual(32);
    const unique = new Set(actionCardColors);
    expect(unique.size).toBe(actionCardColors.length);
  });

  it("picks action color deterministically based on action type", () => {
    const colorBash1 = colorForAction("bash");
    const colorBash2 = colorForAction("bash");
    expect(colorBash1).toEqual(colorBash2);
    expect(colorBash1.hex).toMatch(/^#[0-9a-f]{6}$/i);
    expect(colorBash1.rgb).toMatch(/^\d+,\s*\d+,\s*\d+$/);

    const colorRead = colorForAction("read");
    expect(colorRead.hex).toBeDefined();

    // Verify it maps across different action types
    const types = ["bash", "read", "edit", "write", "subagent", "goal", "reminder"];
    const colors = types.map((t) => colorForAction(t).hex);
    expect(colors.length).toBe(types.length);
  });

  it("renders AlienText component with 3-7 symbols and space tokens", () => {
    const { container } = render(<AlienText input="test-action" minLength={3} maxLength={7} />);
    const symbols = container.querySelectorAll(".alien-symbol");
    expect(symbols.length).toBeGreaterThanOrEqual(3);
    expect(symbols.length).toBeLessThanOrEqual(7);

    for (const sym of symbols) {
      if (sym instanceof HTMLElement) {
        expect(sym.style.maskImage || sym.style.webkitMaskImage).toContain("symbol");
      }
    }
  });

  it("produces diverse symbol counts and space patterns across worker names", () => {
    const workerNames = [
      "worker-1",
      "worker-2",
      "worker-3",
      "supervisor",
      "ui-tweaks",
      "worker-context-bar",
    ];

    const results = workerNames.map((name) => {
      const tokens = generateAlienTokens(name, 3, 7);
      const symbolCount = tokens.filter((t) => t.type === "symbol").length;
      const spaceCount = tokens.filter((t) => t.type === "space").length;
      const pattern = tokens.map((t) => (t.type === "symbol" ? "S" : " ")).join("");
      return { name, symbolCount, spaceCount, pattern };
    });

    // Verify not all worker names share identical symbol count
    const symbolCounts = new Set(results.map((r) => r.symbolCount));
    expect(symbolCounts.size).toBeGreaterThan(1);

    // Verify not all worker names share identical token patterns
    const patterns = new Set(results.map((r) => r.pattern));
    expect(patterns.size).toBeGreaterThan(1);

    // Ensure worker-1, worker-2, and worker-3 do NOT have the exact same token pattern
    const w1 = results.find((r) => r.name === "worker-1")!;
    const w2 = results.find((r) => r.name === "worker-2")!;
    const w3 = results.find((r) => r.name === "worker-3")!;
    const workerPatterns = new Set([w1.pattern, w2.pattern, w3.pattern]);
    expect(workerPatterns.size).toBeGreaterThan(1);
  });
});
