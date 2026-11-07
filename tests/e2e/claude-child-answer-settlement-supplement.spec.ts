import { writeFile } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input, require_catalog: true } });
const text = (value: string) => ({ text: value });
const block = (value: string, id = "answer-message") => ({ emit: { type: "assistant", message: { id, role: "assistant", model: "haiku", content: [{ type: "text", text: value }] } } });
const result = (id: string) => ({ emit: { type: "user", message: { role: "user", content: [{ type: "tool_result", tool_use_id: id, content: "observation" }] } } });

const cases = [
  { name: "multi-block", steps: [block("first "), block("second")], status: "completed", answer: "first second", skip: false, checked: false },
  { name: "later assistant replaces candidate", steps: [block("obsolete candidate"), block("replacement answer", "later-message")], status: "completed", answer: "replacement answer", skip: false, checked: false },
  { name: "tool call invalidates candidate", steps: [block("obsolete candidate"), call("read-1", "read", { path: "note.txt" })], status: "failed", answer: "", skip: false, checked: true },
  { name: "user message invalidates candidate", steps: [block("obsolete candidate"), { emit: { type: "user", message: { role: "user", content: "new user message" } } }], status: "failed", answer: "", skip: false, checked: false },
  { name: "matching", steps: [call("read-1", "read", { path: "note.txt" }), text("checked answer")], status: "completed", answer: "checked answer", skip: false, checked: true },
  { name: "unacknowledged", steps: [call("read-1", "read", { path: "note.txt" }), text("must not settle")], status: "failed", answer: "", skip: true, checked: true },
  { name: "mismatched", steps: [call("read-1", "read", { path: "note.txt" }), result("other-id"), text("must not settle")], status: "failed", answer: "", skip: true, checked: true },
  { name: "unchecked", steps: [result("never-checked"), text("must not settle")], status: "failed", answer: "", skip: false, checked: false },
];

for (const c of cases) test(`CCA settlement: ${c.name}`, async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness({ skip_mcp_result: c.skip, executions_by_input: [
    { contains: "Start answer child", turns: [[call("spawn-answer", "subagent", { prompt: "Do answer child", description: "Answer child" }), text("Child started")]] },
    { contains: "Do answer child", turns: [c.steps] },
    { contains: "finished with status", turns: [[text("Notice received")]] },
  ] }, { defaultModel: null });
  try {
    await writeFile(`${f.router.root}/work/note.txt`, "checked evidence");
    await f.open(page);
    expect((await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "haiku" } }, "PUT")).status).toBe(200);
    const parent = await f.launch(page);
    await f.userTurn(page, "Start answer child");
    await expect.poll(() => f.rows("SELECT status FROM managed_subagents WHERE parent_agent_id=?", parent), { timeout: 30_000 }).toEqual([{ status: c.status }]);
    const childRow = f.rows("SELECT agent_id, result_text, error FROM managed_subagents WHERE parent_agent_id=?", parent)[0];
    const child = String(childRow.agent_id);
    expect(childRow.result_text ?? "").toBe(c.answer);
    if (c.status === "failed") expect(String(childRow.error)).toMatch(/unacknowledged|no checked producer|without current final assistant response/i);
    if (c.checked) expect((await f.records()).filter(r => r.kind === "mcp_call_response" && r.tool_id === "read-1")).toEqual([expect.objectContaining({ error: null })]);
    await expect.poll(() => f.rows("SELECT id, subject, body FROM messages WHERE recipient=? AND id LIKE 'subagent-completed-%'", parent).filter(row => row.id === `subagent-completed-${child}-1`), { timeout: 30_000 }).toEqual([
      expect.objectContaining({ subject: expect.stringContaining(c.status === "completed" ? "Completed" : "Failed"), body: expect.stringContaining(c.status === "completed" ? c.answer : "failed") }),
    ]);
    const noticeMarker = `Task ${child} (Answer child) finished with status ${c.status}:`;
    await expect.poll(async () => (await f.inputs()).map(input => JSON.stringify(input)).find(input => input.includes(noticeMarker)), { timeout: 30_000 }).toBeDefined();
    const notice = (await f.inputs()).map(input => JSON.stringify(input)).find(input => input.includes(noticeMarker))!;
    if (c.status === "completed") {
      expect(notice).toContain(c.answer);
      if (c.name === "later assistant replaces candidate") expect(notice).not.toContain("obsolete candidate");
    } else {
      expect(notice).toContain(String(childRow.error));
      expect(notice).not.toContain(c.name.includes("invalidates") ? "obsolete candidate" : "must not settle");
    }
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
