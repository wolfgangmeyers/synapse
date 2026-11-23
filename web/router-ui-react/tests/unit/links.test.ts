import { describe, expect, it, beforeEach } from "vitest";
import {
  EMPTY_LINKS_STATE, LINKS_PER_AGENT_LIMIT, addManualLink, clearLinks, extractLinks, previewUrl, removeLink, visibleLinks,
  type LinksState,
} from "../../src/domain/links";
import { LINKS_AGENT_LIMIT, getLinksState, resetLinksSession, setLinksState } from "../../src/persistence/links-session";
import type { HistoryMessage } from "../../src/domain/history";

const msg = (id: string, body: string | undefined, at: number, extra: Partial<HistoryMessage> = {}): HistoryMessage =>
  ({ id, ...(body === undefined ? {} : { body }), created_at: new Date(at).toISOString(), ...extra });

describe("extractLinks", () => {
  it("extracts markdown and plain urls, dedupes keeping most recent", () => {
    const links = extractLinks([
      msg("a", "see https://example.com/x.", 1000),
      msg("b", "[Docs](https://docs.dev/a) and https://plain.io/path?q=1", 2000),
      msg("c", "[Again](https://example.com/x)", 3000),
    ]);
    expect(links.map((l) => [l.label, l.messageId])).toEqual([
      ["Again", "c"], ["plain.io/path?q=1", "b"], ["Docs", "b"],
    ]);
  });

  it("does not depend on input order", () => {
    const links = extractLinks([msg("late", "https://a.io/late", 2000), msg("early", "https://a.io/early", 1000)]);
    expect(links.map((l) => l.messageId)).toEqual(["late", "early"]);
  });

  it("keeps an earlier markdown label when a newer message repeats the bare url", () => {
    const links = extractLinks([msg("a", "[Release Notes](https://x.io/r)", 1000), msg("b", "https://x.io/r", 2000)]);
    expect(links).toHaveLength(1);
    expect(links[0]).toMatchObject({ label: "Release Notes", messageId: "b", time: 2000 });
  });

  it("handles balanced parentheses in urls", () => {
    const links = extractLinks([msg("a", "[R](https://en.wikipedia.org/wiki/React_(software)) and https://en.wikipedia.org/wiki/Vue_(x).", 1)]);
    expect(links.map((l) => l.url).sort()).toEqual(["https://en.wikipedia.org/wiki/React_(software)", "https://en.wikipedia.org/wiki/Vue_(x)"]);
  });

  it("does not extract a markdown url twice as a plain url", () => {
    expect(extractLinks([msg("a", "[Only](https://once.io/a)", 1)])).toHaveLength(1);
  });

  it("ignores non-http urls, pending and failed messages, and missing bodies", () => {
    expect(extractLinks([
      msg("a", "[x](javascript:alert(1)) ftp://x.y", 1),
      msg("b", "https://p.io", 2, { pending: true }),
      msg("c", "https://f.io", 3, { failed: true }),
      msg("d", undefined, 4),
    ])).toEqual([]);
  });
});

describe("previewUrl", () => {
  it("drops www and the root slash, and truncates long urls", () => {
    expect(previewUrl("https://www.example.com/path")).toBe("example.com/path");
    expect(previewUrl("https://example.com/")).toBe("example.com");
    const long = previewUrl(`https://example.com/${"a".repeat(80)}`);
    expect(long).toHaveLength(48);
    expect(long.endsWith("…")).toBe(true);
  });
});

describe("links state", () => {
  const ex = () => extractLinks([msg("a", "https://a.io", 1000)]);

  it("removes an extracted link, resurfaces it on a newer mention, and clears all", () => {
    const removed = removeLink(EMPTY_LINKS_STATE, ex()[0]!, ex());
    expect(visibleLinks(ex(), removed)).toEqual([]);
    expect(visibleLinks(extractLinks([msg("a", "https://a.io", 1000), msg("b", "https://a.io", 2000)]), removed)).toHaveLength(1);
    const manual = addManualLink(EMPTY_LINKS_STATE, "https://m.io", "Mine", 5)!;
    expect(visibleLinks(ex(), manual)).toHaveLength(2);
    expect(visibleLinks(ex(), clearLinks(manual, ex()))).toEqual([]);
  });

  it("rejects invalid urls and falls back to a preview label", () => {
    expect(addManualLink(EMPTY_LINKS_STATE, "nope", "")).toBeUndefined();
    expect(addManualLink(EMPTY_LINKS_STATE, "ftp://x.io", "")).toBeUndefined();
    expect(addManualLink(EMPTY_LINKS_STATE, "https://www.example.com/docs", "  ")!.manual[0]!.label).toBe("example.com/docs");
  });

  it("removing a manual link leaves other state alone", () => {
    const state = addManualLink(EMPTY_LINKS_STATE, "https://only.manual", "M", 1)!;
    const next = removeLink(state, state.manual[0]!, []);
    expect(next.manual).toEqual([]);
    expect(next.dismissed).toEqual({});
  });

  it("a newer manual link overrides the extracted label, and re-adding updates in place", () => {
    const first = addManualLink(EMPTY_LINKS_STATE, "https://a.io", "Custom", 5000)!;
    expect(visibleLinks(ex(), first).map((l) => l.label)).toEqual(["Custom"]);
    const second = addManualLink(first, "https://a.io", "Renamed", 6000)!;
    expect(second.manual).toHaveLength(1);
    expect(second.manual[0]!.label).toBe("Renamed");
    // An older manual link does not override a newer message mention.
    const older = addManualLink(EMPTY_LINKS_STATE, "https://a.io", "Old", 10)!;
    expect(visibleLinks(ex(), older)[0]!.label).toBe("a.io");
  });

  it("adding a link the user deleted brings it back", () => {
    const removed = removeLink(EMPTY_LINKS_STATE, ex()[0]!, ex());
    const readded = addManualLink(removed, "https://a.io", "Back", 9999)!;
    expect(readded.dismissed).toEqual({});
    expect(visibleLinks(ex(), readded).map((l) => l.label)).toEqual(["Back"]);
  });

  it("deleting a manual link that shadows a message link removes both", () => {
    const manual = addManualLink(EMPTY_LINKS_STATE, "https://a.io", "Mine", 9999)!;
    const shown = visibleLinks(ex(), manual);
    expect(shown).toHaveLength(1);
    const removed = removeLink(manual, shown[0]!, ex());
    expect(visibleLinks(ex(), removed)).toEqual([]);
  });
});

describe("retention", () => {
  beforeEach(() => resetLinksSession());

  it("caps manual links per agent", () => {
    let state: LinksState = EMPTY_LINKS_STATE;
    for (let i = 0; i < LINKS_PER_AGENT_LIMIT + 10; i++) state = addManualLink(state, `https://e.io/${i}`, "", i)!;
    expect(state.manual).toHaveLength(LINKS_PER_AGENT_LIMIT);
    expect(state.manual[0]!.url).toBe(`https://e.io/${LINKS_PER_AGENT_LIMIT + 9}`);
  });

  it("caps the dismissed map, keeping the newest dismissals", () => {
    const many = extractLinks(Array.from({ length: 1100 }, (_, i) => msg(`m${i}`, `https://d.io/${i}`, i)));
    const cleared = clearLinks(EMPTY_LINKS_STATE, many);
    expect(Object.keys(cleared.dismissed)).toHaveLength(LINKS_PER_AGENT_LIMIT * 5);
    expect(cleared.dismissed["https://d.io/1099"]).toBe("m1099");
    expect(cleared.dismissed["https://d.io/0"]).toBeUndefined();
  });

  it("evicts the least recently used agent", () => {
    const state: LinksState = addManualLink(EMPTY_LINKS_STATE, "https://s.io", "", 1)!;
    for (let i = 0; i < LINKS_AGENT_LIMIT + 5; i++) setLinksState(`agent-${i}`, state);
    expect(getLinksState("agent-0")).toBe(EMPTY_LINKS_STATE);
    expect(getLinksState(`agent-${LINKS_AGENT_LIMIT + 4}`)).toBe(state);
  });
});
