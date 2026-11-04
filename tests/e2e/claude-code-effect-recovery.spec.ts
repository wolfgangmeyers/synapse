import { test, expect } from "@playwright/test";
import { effectBarrier, recoveryLiveEvidence, type EffectReceipt } from "./claude-code-effect-recovery-harness";

// SPEC-LIFECYCLE-001 CC-007: the first tests exercise the barrier with synthetic
// receipts. The crash slice below exercises real mail effects at intent, domain,
// and checked phases across router restart. It
// does not cover MCP replay or claim complete coverage of other effect types.
const scope: EffectReceipt = { phase: "intent", agent_id: "transport-test", session_id: "session",
  epoch: 1, generation: "generation", owner_token: 1, effect_id: "canonical-test-effect" };

test("CC-007 infrastructure holds one exact scoped receipt until explicit release", async () => {
  const barrier = await effectBarrier();
  const requests: Promise<Response>[] = [];
  const track = (request: Promise<Response>) => {
    void request.catch(() => {}); requests.push(request); return request;
  };
  try {
    barrier.arm(scope);
    let settled = false;
    const response = track(fetch(barrier.endpoint + "/barrier", { method: "POST",
      headers: { Authorization: `Bearer ${barrier.token}` }, body: JSON.stringify(scope),
      signal: AbortSignal.timeout(3000) }).then(value => { settled = true; return value; }));
    expect(await barrier.arrived).toEqual(scope);
    expect(settled).toBe(false); // receipt precedes release, no sleep/poll-to-kill
    barrier.assertHeld();
    barrier.release(); expect((await response).status).toBe(204);
    expect(await barrier.finished).toBe("released");
    expect(() => barrier.assertHeld()).toThrow("not held pending");
    const duplicate = await fetch(barrier.endpoint + "/barrier", { method: "POST",
      headers: { Authorization: `Bearer ${barrier.token}` }, body: JSON.stringify(scope) });
    expect(duplicate.status).toBe(409);
  } finally { await barrier.close(); await Promise.allSettled(requests); }
});

test("CC-007 infrastructure rejects wrong token or canonical identity without consuming barrier", async () => {
  const barrier = await effectBarrier();
  const requests: Promise<Response>[] = [];
  const track = (request: Promise<Response>) => {
    void request.catch(() => {}); requests.push(request); return request;
  };
  try {
    barrier.arm(scope);
    for (const [token, receipt, status] of [
      ["wrong", scope, 403],
      [barrier.token, { ...scope, effect_id: "different-incarnation" }, 409],
      [barrier.token, { ...scope, owner_token: 2 }, 409],
    ] as const) {
      const response = await fetch(barrier.endpoint + "/barrier", { method: "POST",
        headers: { Authorization: `Bearer ${token}` }, body: JSON.stringify(receipt) });
      expect(response.status).toBe(status);
    }
    const response = track(fetch(barrier.endpoint + "/barrier", { method: "POST",
      headers: { Authorization: `Bearer ${barrier.token}` }, body: JSON.stringify(scope) }));
    expect(await barrier.arrived).toEqual(scope);
    barrier.release(); expect((await response).status).toBe(204);
  } finally { await barrier.close(); await Promise.allSettled(requests); }
});

// CC-007 real-boundary crash slice. Does NOT claim MCP replay: original-effect
// evidence is inspected by the receipt's saved canonical identity.
// No fabricated rows/triggers, no same-raw-ID retry under a new incarnation.
for (const phase of ["intent", "domain", "checked"] as const) {
  test(`CC-007 ${phase} crash preserves original effect certainty across same-DB recovery`, async ({ page }) => {
    test.setTimeout(90_000);
    const { claudeWorkersHarness } = await import("./claude-workers-harness");
    const { crashAndRestart, inspectOriginalEffect } = await import("./claude-code-effect-recovery-harness");
    const { writeFile, rename, rm } = await import("node:fs/promises");
    let f: Awaited<ReturnType<typeof claudeWorkersHarness>> | undefined;
    let live: ReturnType<typeof recoveryLiveEvidence> | undefined;
    const barrier = await effectBarrier();
    try {
      f = await claudeWorkersHarness({ turns: [[{ text: "Ready before controlled effect" }]] });
      await f.open(page); const agent = await f.launch(page);
      await f.userTurn(page, "Establish the checked foreground binding");
      await f.final(page, agent, "Ready before controlled effect");
      const row = f.tableRows("embedded_sessions", agent).find(row => row.current === 1)!;
      barrier.armBinding({ phase, agent_id: agent, session_id: String(row.session_id), epoch: Number(row.epoch),
        generation: String(row.generation), owner_token: Number(row.owner_token) });
      const configPath = f.router.databasePath + ".effect-barrier.json";
      await writeFile(configPath + ".new", JSON.stringify({ enabled: "1", endpoint: barrier.endpoint, token: barrier.token, agent_id: agent, phase }), { mode: 0o600 });
      await rename(configPath + ".new", configPath);
      // configure() affects the next process, so restart the worker before the
      // effect turn. The receipt must come from this actual checked producer.
      const body = `Controlled CC007 mail ${phase} ${agent}`;
      await f.configure({ turns_by_input: [
        { contains: "Confirm restart readiness", steps: [{ text: "Restart readiness confirmed" }] },
        { contains: "Send exactly one controlled message", steps: [{ mcp_call: { id: "effect-call", name: "mecha_mailbox_send",
          input: { recipient: "supervisor", body } } }, { text: "Effect turn finished" }] },
      ] });
      expect((await f.request(page, `/v1/managed-agents/${agent}/restart`, {})).status).toBe(202);
      // Restart is asynchronous and lifecycle_state stays running. A completed
      // prompt proves the restart admission gate reopened and the new process ran.
      await expect.poll(async () => {
        const response = await f!.request(page, `/v1/managed-agents/${agent}/prompt`, { message: "Confirm restart readiness" });
        if (response.status !== 200 && response.status !== 409) throw Error(`restart readiness prompt: ${response.status}`);
        return response.status;
      }).toBe(200);
      await f.final(page, agent, "Restart readiness confirmed");
      await f.userTurn(page, "Send exactly one controlled message");
      const receipt = await barrier.arrived;
      const oldGrant = await f.grant();
      // The real public router-proxy must not expose the private MCP route,
      // even when presented with the actual process grant.
      const publicAttempt = await fetch(new URL('/v1/mcp', f.proxy.url), { method: 'POST',
        headers: { ...oldGrant.headers, 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 'public-probe', method: 'tools/call',
          params: { name: 'mecha_mailbox_send', arguments: { recipient: 'supervisor', body },
            _meta: { 'claudecode/toolUseId': 'public-probe' } } }), signal: AbortSignal.timeout(5000) });
      expect(publicAttempt.status).not.toBe(200);
      expect(receipt.effect_id).toMatch(/^claude-[0-9a-f]{64}$/);
      const original = inspectOriginalEffect(f.router.databasePath, receipt);
      expect(original?.state).toBe(phase === "checked" ? "observed" : "unknown");
      const countEffects = () => f!.rows("SELECT body FROM messages WHERE sender=?", agent).filter(row => row.body === body).length;
      expect(countEffects()).toBe(phase === "intent" ? 0 : 1);
      const result = typeof original?.result === "string" ? JSON.parse(original.result) : undefined;
      if (phase === "checked") {
        expect(result.toolCallId).toBe(receipt.effect_id);
        expect(result.isError ?? false).toBe(false);
        const checked = f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)
          .map(row => JSON.parse(String(row.body))).find(row => row.toolCallId === receipt.effect_id);
        expect(checked).toEqual(result);
        const content = JSON.parse(result.content[0].text);
        expect(content.status).toBe("accepted");
        expect(f.rows("SELECT id,body FROM messages WHERE sender=?", agent)
          .some(row => row.id === content.message_id && row.body === body)).toBe(true);
      } else expect(original?.result).toBeNull();
      // Intent can precede any entry in its binding. Preserve the complete
      // already-persisted conversation prefix, including earlier bindings.
      const persistedEntries = () => f!.rows(`SELECT session_id,epoch,generation,seq,parent_digest,digest,kind,CAST(body AS TEXT) AS body FROM embedded_entries
        WHERE agent_id=? ORDER BY epoch,session_id,generation,seq`, receipt.agent_id);
      const beforeEntries = persistedEntries();
      expect(beforeEntries.some(entry => entry.kind === "assistant" && String(entry.body).includes("Ready before controlled effect"))).toBe(true);
      await rm(configPath); // no re-arm after crash; old effect is evidence only
      const foreground = "Continue without repeating any previous tool effect";
      const foregroundAnswer = "Recovery foreground settled without tool execution";
      await f.configure({ turns: [[{ text: "Recovery notice settled" }]],
        turns_by_input: [{ contains: foreground, steps: [{ text: foregroundAnswer }] }] });
      await crashAndRestart(f.router, barrier);
      // A checked foreground turn is the positive recovery-settled boundary;
      // assertions are not absence during a sleep or a poll-to-kill race.
      live = recoveryLiveEvidence(page, f.proxy.url, agent, foregroundAnswer);
      await page.goto(f.frontend.url);
      await page.locator(`[data-agent-id="${agent}"]`).click();
      await live.ready();
      await f.userTurn(page, foreground);
      await f.final(page, agent, foregroundAnswer);
      await live.final();
      // The checked final must descend from this foreground input, not the
      // startup recovery notice which may consume the first scripted turn.
      const entries = f.rows("SELECT session_id,epoch,generation,seq,parent_digest,digest,kind,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent);
      const final = entries.find(row => row.kind === "assistant" && String(row.body).includes(foregroundAnswer));
      expect(final).toBeDefined();
      let ancestor = final;
      while (ancestor && ancestor.kind !== "user") {
        const parent = ancestor.parent_digest;
        ancestor = entries.find(row => row.session_id === final!.session_id && row.epoch === final!.epoch && row.generation === final!.generation && row.digest === parent);
      }
      expect(ancestor).toBeDefined();
      expect(String(ancestor!.body)).toContain(foreground);
      // The crashed router owned the old private listener. A dead listener
      // refuses the connection rather than returning an HTTP authorization error.
      const oldEndpoint = new URL(oldGrant.url);
      expect(oldEndpoint.hostname).toBe('127.0.0.1');
      let oldStatus: number | undefined;
      let failure: unknown;
      try {
        const response = await fetch(oldGrant.url, { method: 'POST',
          headers: { ...oldGrant.headers, 'Content-Type': 'application/json' },
          body: JSON.stringify({ jsonrpc: '2.0', id: 'old-probe', method: 'tools/call',
            params: { name: 'mecha_mailbox_send', arguments: { recipient: 'supervisor', body },
              _meta: { 'claudecode/toolUseId': 'effect-call' } } }), signal: AbortSignal.timeout(5000) });
        oldStatus = response.status;
      } catch (error) { failure = error; }
      if (failure) {
        expect((failure as Error & { cause?: { code?: string } }).cause?.code).toBe('ECONNREFUSED');
      } else {
        // A restarted listener can theoretically reuse the old port.
        expect(oldStatus).toBe(401);
      }
      const afterEntries = persistedEntries();
      for (const entry of beforeEntries) {
        expect(afterEntries.find(row => row.session_id === entry.session_id && row.epoch === entry.epoch &&
          row.generation === entry.generation && row.seq === entry.seq)).toEqual(entry);
      }
      expect(inspectOriginalEffect(f.router.databasePath, receipt)).toEqual(original);
      expect(countEffects()).toBe(phase === "intent" ? 0 : 1);
      expect(f.violations).toEqual([]);
    } finally { live?.close(); await barrier.close(); await f?.close(); }
  });
}
