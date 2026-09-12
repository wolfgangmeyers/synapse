import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { terminateManagedTestAgents } from "../web/router-ui-react/tests/support/managed-agent-cleanup.mjs";

function response(status: number, body?: unknown) {
  return new Response(body === undefined ? undefined : JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

test("the release command uses the Browser Console Bridge managed-RPC gate", async () => {
  const root = resolve(import.meta.dirname, "..");
  const packageJson = JSON.parse(await readFile(resolve(root, "package.json"), "utf8")) as { scripts?: Record<string, string> };
  const command = packageJson.scripts?.["test:mecha-rpc:release-e2e"];
  assert.equal(command, "node web/router-ui-react/tests/acceptance/bcb-runner.mjs --managed-rpc --release");
});

test("managed acceptance cleanup terminates owned agents and waits for durable removal", async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = [];
  let reads = 0;
  const results = await terminateManagedTestAgents({
    baseUrl: "http://router.test",
    agentIDs: ["proof-agent"],
    fetchImpl: async (url: string | URL | Request, init?: RequestInit) => {
      requests.push({ url: String(url), init });
      if (init?.method === "DELETE") return response(202);
      reads += 1;
      return reads === 1 ? response(200, { agent: { process_pid: 4321 } }) : response(404);
    },
    assertProcessAbsent: ({ agentID, processPID }: { agentID: string; processPID?: number }) => {
      assert.equal(agentID, "proof-agent");
      assert.equal(processPID, 4321);
      return { absent: true };
    },
  });

  assert.equal(results.length, 1);
  assert.equal(results[0].agent_id, "proof-agent");
  assert.deepEqual(results[0].process, { absent: true });
  const termination = requests.find(({ init }) => init?.method === "DELETE");
  assert.equal(termination === undefined, false);
  assert.match((termination?.init?.headers as Record<string, string>)["X-Request-ID"], /^[0-9a-f-]{36}$/);
});

test("managed acceptance cleanup still attempts every owned agent after a failure", async () => {
  const deleted: string[] = [];
  await assert.rejects(
    terminateManagedTestAgents({
      baseUrl: "http://router.test",
      agentIDs: ["fails", "still-cleaned"],
      fetchImpl: async (url: string | URL | Request, init?: RequestInit) => {
        const agentID = String(url).split("/").at(-1)!;
        if (init?.method === "DELETE") {
          deleted.push(agentID);
          return response(agentID === "fails" ? 500 : 202);
        }
        if (agentID === "still-cleaned" && deleted.includes(agentID)) return response(404);
        return response(200, { agent: { process_pid: 4321 } });
      },
      assertProcessAbsent: () => ({ absent: true }),
    }),
    { name: "AggregateError" },
  );
  assert.deepEqual(deleted, ["fails", "still-cleaned"]);
});
