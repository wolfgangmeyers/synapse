import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, type ClaudeWorkersHarness } from "./claude-workers-harness";

// Public browser/router boundaries and deterministic local Claude CLI only.
// Read-only SQLite checks establish what was actually persisted, not fixtures.
const answer = (text: string) => ({ text });
const tool = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });
const instruction = "Seed instruction: compass 🧭 north";
const note = "Pinned seed memory: violet 紫";

function arg(row: Record<string, unknown>, flag: string) {
  if (!Array.isArray(row.argv)) throw Error("missing fixture argv");
  const index = row.argv.indexOf(flag);
  if (index < 0 || typeof row.argv[index + 1] !== "string") throw Error(`missing ${flag}`);
  return row.argv[index + 1];
}

test.describe("Claude public seed boundaries", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  test.afterEach(async () => { await f?.close(); f = undefined; });

  test("fresh empty history still sends the instruction in the seed block", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[answer("Initial seed observed")]] });
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: instruction });
    await f.final(page, agent, "Initial seed observed");
    const inputs = await f.inputs();
    expect(inputs).toHaveLength(1);
    const blocks = inputs[0].message.content;
    expect(Array.isArray(blocks)).toBe(true);
    expect(blocks[0].type).toBe("text");
    expect(blocks[0].text).toBe(`Recent conversation preserved by the router:\nInstruction:\n${instruction}\n`);
    expect(f.violations).toEqual([]);
  });

  test("missing transcript reseeds a genuinely pinned note and checked structured tool arguments", async ({ page }) => {
    const history = "Prior answer: jade 玉";
    const marker = "structured-argument-amber-42";
    const wake = "Continue after missing transcript";
    f = await claudeWorkersHarness({ turns: [[
      tool("pin-seed-note", "scratchpad", { action: "append", content: note, pinned: true }),
      { tool: { id: "seed-native-read", name: "Read", input: { file_path: marker }, result: "fixture read result" } },
      answer(history),
    ]] });
    await f.open(page);
    const agent = await f.launch(page, { initial_instruction: instruction });
    await f.final(page, agent, history);
    const notes = f.tableRows("scratchpad_entries", agent);
    expect(notes).toHaveLength(1);
    expect(notes[0].content).toBe(note);
    expect(Number(notes[0].pinned)).toBe(1);
    const checked = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent);
    // Only claim tool-argument coverage if the router really stored it as structured history.
    const readCall = (messages: { content?: { type?: string; id?: string; name?: string; arguments?: object }[] }[]) =>
      messages.flatMap(message => Array.isArray(message.content) ? message.content : [])
        .find(part => part.type === "toolCall" && part.id === "seed-native-read" && part.name === "Read");
    const storedCall = readCall(checked.map(row => JSON.parse(String(row.body))));
    expect(storedCall?.arguments).toEqual({ file_path: marker });
    const oldSession = arg((await f.starts())[0], "--session-id");
    await f.configure({ resume_missing: true, turns: [[answer("Reseed completed")]] });
    await f.userTurn(page, wake);
    await f.final(page, agent, "Reseed completed");
    const starts = await f.starts();
    expect(starts).toHaveLength(3);
    expect(arg(starts[1], "--resume")).toBe(oldSession);
    expect(starts[2].argv).not.toContain("--resume");
    const blocks = (await f.inputs()).at(-1).message.content;
    expect(blocks[0].type).toBe("text");
    const seed: string = blocks[0].text;
    const prefix = `Recent conversation preserved by the router:\nInstruction:\n${instruction}\nPersistent Scratchpad Memory:\n- [Pinned] ${note}\n\n`;
    expect(seed.startsWith(prefix)).toBe(true);
    const seededHistory = seed.slice(prefix.length).trim().split("\n").map(line => JSON.parse(line));
    expect(readCall(seededHistory)?.arguments).toEqual(storedCall?.arguments);
    let previous = -1;
    for (const part of [instruction, `[Pinned] ${note}`, marker, history]) {
      const index = seed.indexOf(part);
      expect(index, `seed order: ${part}`).toBeGreaterThan(previous);
      previous = index;
    }
    expect(JSON.stringify(blocks.slice(1))).toContain(wake);
    expect(f.violations).toEqual([]);
  });
});
