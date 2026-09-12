import { randomUUID } from "node:crypto";

const DEFAULT_TIMEOUT_MS = 15_000;
const POLL_INTERVAL_MS = 100;

function delay(ms) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, ms));
}

async function waitForRemoval(fetchImpl, baseUrl, agentID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let lastStatus;
  while (Date.now() < deadline) {
    const response = await fetchImpl(`${baseUrl}/v1/managed-agents/${encodeURIComponent(agentID)}`);
    lastStatus = response.status;
    if (response.status === 404) return;
    await delay(POLL_INTERVAL_MS);
  }
  throw new Error(`managed agent ${agentID} remained after termination (last status ${lastStatus ?? "unavailable"})`);
}

/**
 * Tear down only the managed agents explicitly owned by an acceptance run.
 * The router's termination endpoint proves exact process-group cleanup before
 * it deletes the durable row; the final 404 confirms that cleanup finished.
 */
export async function terminateManagedTestAgents({
  baseUrl,
  agentIDs,
  fetchImpl = fetch,
  timeoutMs = DEFAULT_TIMEOUT_MS,
  assertProcessAbsent,
}) {
  const results = [];
  const failures = [];

  for (const agentID of [...new Set(agentIDs)]) {
    try {
      const existing = await fetchImpl(`${baseUrl}/v1/managed-agents/${encodeURIComponent(agentID)}`);
      if (existing.status === 404) {
        results.push({ agent_id: agentID, already_removed: true });
        continue;
      }
      if (!existing.ok) throw new Error(`could not inspect managed agent ${agentID}: HTTP ${existing.status}`);
      const payload = await existing.json();
      const processPID = payload?.agent?.process_pid;
      const requestID = randomUUID();
      const termination = await fetchImpl(`${baseUrl}/v1/managed-agents/${encodeURIComponent(agentID)}`, {
        method: "DELETE",
        headers: { "X-Request-ID": requestID },
      });
      if (termination.status !== 202 && termination.status !== 404) {
        throw new Error(`could not terminate managed agent ${agentID}: HTTP ${termination.status}`);
      }
      await waitForRemoval(fetchImpl, baseUrl, agentID, timeoutMs);
      const process = assertProcessAbsent ? await assertProcessAbsent({ agentID, processPID }) : undefined;
      results.push({ agent_id: agentID, request_id: requestID, process, already_removed: termination.status === 404 });
    } catch (error) {
      failures.push(error instanceof Error ? error : new Error(String(error)));
    }
  }

  if (failures.length > 0) {
    throw new AggregateError(failures, `managed acceptance cleanup failed for ${failures.length} agent(s)`);
  }
  return results;
}
