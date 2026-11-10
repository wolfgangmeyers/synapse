import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { claudeWorkersHarness } from "./claude-workers-harness";

test.skip(process.platform !== "linux", "Background bash launch is supported only on Linux");

test("short completions steer during active launches", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  const call = (id: string) => ({ mcp_call: { id, name: "launch_background_bash", input: { command: `printf ${id}` }, require_catalog: true } });
  try {
    try {
      await f.configure({ steer_mid_turn: true, turns_by_input: [
        { contains: "Steer seed", steps: [call("seed"), { text: "Seed finished" }] },
        { contains: "Steer batch", steps: [call("batch-0"), { barrier: "batch-active" }, ...Array.from({ length: 9 }, (_, i) => call(`batch-${i + 1}`)), { steer: "Notices handled" }, { text: "Batch finished" }] },
      ] });
      await f.open(page);
      const agent = await f.launch(page);
      await f.userTurn(page, "Steer seed");
      await f.final(page, agent, "Seed finished", undefined, false);
      await f.userTurn(page, "Steer batch");
      let barrier: Record<string, unknown> | undefined;
      await expect.poll(async () => {
        barrier = (await f.records()).find(row => row.kind === "barrier" && row.name === "batch-active");
        return !!barrier;
      }, { timeout: 30_000 }).toBe(true);
      const results = () => f.rows("SELECT seq, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result') ORDER BY seq", agent)
        .map(row => ({ seq: Number(row.seq), body: JSON.parse(String(row.body)) }))
        .filter(row => row.body.toolName === "launch_background_bash");
      await expect.poll(() => results().length).toBe(2);
      const first = results()[1];
      expect(first, JSON.stringify(results())).toBeDefined();
      const jobId = JSON.parse(first!.body.content[0].text).job_id;
      expect(jobId).toEqual(expect.any(String));
      const notice = `Background bash job ${jobId} finished: succeeded (exit code 0)`;
      const messageId = `background-bash-completed-${jobId}`;
      let inputId: string | undefined;
      await expect.poll(async () => {
        const input = (await f.inputs()).find(item => JSON.stringify(item.message?.content).includes(notice));
        inputId = input?.uuid;
        return inputId && (await f.records()).some(row => row.kind === "steer_echo" && row.input_id === inputId);
      }, { timeout: 30_000, message: "batch job notice echoed while batch turn is held" }).toBe(true);
      let ackSeq = 0;
      await expect.poll(() => {
        const rows = f.rows(`SELECT k.entry_seq, a.accepted_body, m.state, CAST(e.body AS TEXT) AS entry_body
          FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id
          JOIN messages m ON m.id=a.message_id
          JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.generation=k.generation AND e.seq=k.entry_seq
          WHERE a.agent_id=? AND a.message_id=?`, agent, messageId);
        if (rows.length !== 1) return rows;
        ackSeq = Number(rows[0].entry_seq);
        return { state: rows[0].state, accepted: rows[0].accepted_body, persisted: String(rows[0].entry_body).includes(notice) };
      }, { timeout: 15_000 }).toEqual({ state: "acknowledged", accepted: notice, persisted: true });
      expect(ackSeq).toBeGreaterThan(first!.seq);
      await f.releaseBarrier(barrier!);
      await f.final(page, agent, "Batch finished", undefined, false);
      const all = results();
      const calls = (await f.records()).filter(row => row.kind === "mcp_call_response" && row.name === "launch_background_bash");
      expect(calls.map(row => row.tool_id)).toEqual(["seed", ...Array.from({ length: 10 }, (_, i) => `batch-${i}`)]);
      expect(calls.every(row => row.error === null && (row.result as { isError?: boolean }).isError !== true)).toBe(true);
      // Claude canonicalizes fixture IDs per process. Match results to their checked
      // producers, identified by the exact command sent for each fixture ID.
      const producers = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq", agent)
        .flatMap(row => JSON.parse(String(row.body)).content ?? [])
        .filter(part => part.type === "toolCall" && part.name === "launch_background_bash");
      const fixtureIds = ["seed", ...Array.from({ length: 10 }, (_, i) => `batch-${i}`)];
      expect(producers.map(part => part.arguments.command)).toEqual(fixtureIds.map(id => `printf ${id}`));
      expect(all.map(row => row.body.toolCallId)).toEqual(producers.map(part => part.id));
      expect(all.map(row => JSON.parse(row.body.content[0].text).job_id)).toEqual(calls.map(row => JSON.parse((row.result as { content: { text: string }[] }).content[0].text).job_id));
      expect(all.every(row => row.body.isError === false)).toBe(true);
      expect(all[2].seq).toBeGreaterThan(ackSeq);
      expect((await f.records()).some(row => row.kind === "steer_absorbed" && row.input_id === inputId && row.turn_input_id === barrier!.input_id)).toBe(true);
      expect(f.violations).toEqual([]);
    } finally {
      const log = await readFile(f.router.logPath, "utf8");
      const fixtureLog = await readFile(`${f.fixtureRoot}/record.jsonl`).catch(() => Buffer.from(""));
      await testInfo.attach("router.jsonl", { body: Buffer.from(log), contentType: "application/x-ndjson" });
      await testInfo.attach("fixture.jsonl", { body: fixtureLog, contentType: "application/x-ndjson" });
      await testInfo.attach("router-stderr.txt", { body: Buffer.from(f.router.stderr()), contentType: "text/plain" });
      // Structured events are filtered by the router logger; absence of raw
      // error text here alone does not prove the underlying error did not occur.
      const logRecords = log.split("\n").filter(Boolean).map(line => JSON.parse(line));
      expect(logRecords.length).toBeGreaterThan(0);
      expect(JSON.stringify(logRecords)).not.toContain("embedded head mismatch");
      expect(JSON.stringify(logRecords)).not.toContain("tool outcome UNKNOWN after execution");
      // This is only the currently retained tail, not the full stderr stream.
      const stderrTail = f.router.stderr();
      for (const signature of ["embedded head mismatch", "tool outcome UNKNOWN after execution", "event log write failed"]) {
        expect(stderrTail, "retained router stderr tail").not.toContain(signature);
      }
    }
  } finally {
    await f.close();
  }
});
