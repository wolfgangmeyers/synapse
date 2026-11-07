import { readFileSync } from "node:fs";
import { readdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness, exists } from "./claude-workers-harness";
import { model } from "./workflow-implicit-goals-harness";

// AC7/AC12: real router SIGKILL, REST/proxy/browser/SQLite via existing harness.
function stat(pid: number) {
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    return { state: fields[0], start: fields[19], group: Number(fields[2]) };
  } catch (error) { if (error.code === "ENOENT" || error.code === "ESRCH") return undefined; throw error; }
}

for (const mismatch of [false, true]) test(`CC-009/015 ${mismatch ? "mismatched orphan fences Claude while Pi remains available" : "verified orphan is cleaned up and Claude remains available"}`, async ({ page }) => {
  test.skip(process.platform !== "linux", "Linux durable boot/process identity");
  test.setTimeout(90_000);
  const f = await claudeWorkersHarness({ ignore_sighup: true, turns: [[{ spawn_child: {} }, { text: "Partial before crash" }, { wait: "interrupt" }]] });
  let owned: { pid: number; start: string | undefined } | undefined;
  try {
    await f.open(page);
    await f.launch(page);
    await f.userTurn(page, "Start bounded background child before router crash");
    await expect.poll(async () => (await f.records()).some(row => row.kind === "child")).toBe(true);
    const child = (await f.records()).findLast(row => row.kind === "child")!;
    const pid = Number(child.parent_pid), childPID = Number(child.pid);
    expect(Number.isSafeInteger(pid) && pid > 1).toBe(true);
    const identity = stat(pid)!;
    expect(identity.group).toBe(pid);
    owned = { pid, start: identity.start };
    const grant = await f.grant();
    const sessions = join(f.router.root, "managed-home", "sessions");
    const paths = (await readdir(join(sessions, "claude-prompts"))).map(name => join(sessions, "claude-prompts", name));
    expect(paths.length).toBeGreaterThan(0);
    expect((await readdir(join(sessions, "claude-orphans"))).length).toBe(1);
    // Hold the owned group across pipe EOF. Without this deterministic barrier
    // the fixture may voluntarily exit before startup recovery can be tested.
    expect(stat(pid)?.start).toBe(owned.start);
    const childIdentity = stat(childPID)!;
    expect(childIdentity.group).toBe(pid);
    process.kill(-pid, "SIGSTOP");
    // kill() only queues SIGSTOP. Confirm both exact processes have stopped
    // before closing their pipes by killing the router.
    await expect.poll(() => {
      const leader = stat(pid), descendant = stat(childPID);
      return leader?.start === identity.start && descendant?.start === childIdentity.start &&
        leader.group === pid && descendant.group === pid &&
        /^[Tt]$/.test(leader.state ?? "") && /^[Tt]$/.test(descendant.state ?? "");
    }).toBe(true);
    const router = f.router.process;
    const exited = new Promise<void>(resolve => router.once("close", () => resolve()));
    router.kill("SIGKILL");
    await exited;
    // Linux sends SIGHUP and SIGCONT when the stopped group becomes orphaned.
    // The fixture ignores SIGHUP so recovery, not hangup, kills the group.
    expect(stat(pid)?.start).toBe(identity.start);
    expect(stat(childPID)?.start).toBe(childIdentity.start);
    expect(stat(pid)?.state).not.toMatch(/[ZX]/);
    expect(stat(childPID)?.state).not.toMatch(/[ZX]/);
    expect(await exists(grant.path)).toBe(true);
    if (mismatch) {
      const names = await readdir(join(sessions, "claude-orphans"));
      const recordPath = join(sessions, "claude-orphans", names[0]);
      const record = JSON.parse(await readFile(recordPath, "utf8"));
      record.Identity.StartMarker += "-mismatch";
      await writeFile(recordPath, JSON.stringify(record));
    }
    await f.configure({ turns: [[{ text: "Recovered after orphan sweep" }]] });
    await f.restart();
    // Startup rechecks the recorded identity, kills the matching group and
    // confirms it is no longer executable before admitting Claude.
    expect((await fetch(f.router.httpUrl + "/v1/health")).status).toBe(200);
    if (!mismatch) await expect.poll(() => {
      const leader = stat(pid), descendant = stat(childPID);
      return (!leader || (leader.start === identity.start && ["Z", "X"].includes(leader.state ?? ""))) &&
        (!descendant || (descendant.start === childIdentity.start && ["Z", "X"].includes(descendant.state ?? "")));
    }).toBe(true);
    expect((await readdir(join(sessions, "claude-orphans"))).length).toBe(mismatch ? 1 : 0);
    if (mismatch) {
      for (const [processPID, original] of [[pid, identity], [childPID, childIdentity]] as const) {
        const current = stat(processPID);
        expect(current, `original process ${processPID} must still exist`).toBeDefined();
        expect(current?.start).toBe(original.start);
        expect(current?.state).not.toMatch(/[ZX]/);
        expect(current?.group).toBe(pid);
      }
    }
    expect(await exists(grant.path)).toBe(false);
    for (const path of paths) expect(await exists(path)).toBe(false);
    if (mismatch) {
      const startsBefore = (await f.starts()).length;
      const rejected = await f.request(page, "/v1/managed-agents", { name: "fenced-claude", cwd: `${f.router.root}/work`, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" } }, "POST", true);
      expect(rejected.status).toBeGreaterThanOrEqual(400);
      expect((await f.starts()).length).toBe(startsBefore);
    } else {
    const recoveredAgent = await f.launch(page);
    await f.userTurn(page, "Confirm Claude recovered");
    await f.final(page, recoveredAgent, "Recovered after orphan sweep", undefined, false);
    }
    f.replies.push({ answer: "Pi remains available" });
    const pi = await f.request(page, "/v1/managed-agents", {
      name: "available-pi", cwd: `${f.router.root}/work`, runtime: "embedded", model,
    }, "POST", true);
    expect(pi.status, JSON.stringify(pi.body)).toBe(202);
    await expect(page.locator('[data-agent-id="available-pi"]')).toBeVisible();
    await page.locator('[data-agent-id="available-pi"]').click();
    await f.userTurn(page, "Confirm Pi still works");
    await f.final(page, "available-pi", "Pi remains available", undefined, false);
    expect(f.violations).toEqual([]);
  } finally {
    if (owned && stat(owned.pid)?.start === owned.start && !["Z", "X"].includes(stat(owned.pid)?.state ?? "")) {
      try { process.kill(-owned.pid, "SIGKILL"); } catch (error) { if (error.code !== "ESRCH") throw error; }
    }
    await f.close();
  }
});
