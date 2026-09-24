import { performance } from "node:perf_hooks";

export const DEADLINE_MS = 30_000;
const PERSISTED_RECEIPT_KEYS = Object.freeze([
  "agent_id",
  "event",
  "observed_monotonic_ms",
  "process_incarnation",
  "provider_request_seq",
  "receipt_id",
  "runtime_generation",
  "token",
  "trial_id",
]);
const PERSISTED_BARRIER_KEYS = Object.freeze([
  "acknowledged",
  "acknowledgement_id",
  "agent_id",
  "event",
  "fence",
  "observed_monotonic_ms",
  "provider_request_seq",
  "request_active",
  "router_pid",
  "token",
  "trial_id",
]);
const PRIVATE_PUBLIC_KEY = /(?:^|_)(?:pid|session_id|process_identity|incarnation|generation)(?:$|_)/u;
const monotonicNow = () => performance.now();

function requireNonEmpty(value, label) {
  if (typeof value !== "string" || value.length === 0) throw new Error(`${label} must be a non-empty string`);
  return value;
}

function stable(value) { return JSON.stringify(value); }

export function parseManagedRecoveryArguments(arguments_) {
  if (arguments_.length !== 3 || arguments_[0] !== "--managed-recovery" || arguments_[1] !== "--trials" || arguments_[2] !== "5") {
    throw new Error("Usage: test:acceptance:bcb --managed-recovery --trials 5");
  }
  return { managedRecovery: true, trials: 5 };
}

export function signalRouterAtBoundary(child, now = monotonicNow) {
  if (!child || !Number.isInteger(child.pid) || child.pid < 2 || typeof child.kill !== "function") throw new Error("isolated router process is unavailable");
  const restart_boundary = now();
  let signalled;
  try {
    signalled = child.kill("SIGTERM");
  } catch (error) {
    throw new Error(`isolated router SIGTERM threw: ${error instanceof Error ? error.message : String(error)}`);
  }
  if (signalled !== true) throw new Error("isolated router SIGTERM returned false");
  return { restart_boundary, deadline: restart_boundary + DEADLINE_MS };
}

export class ManagedRecoveryTimeoutError extends Error {
  constructor(timeoutEvidence, cleanup) {
    super(`managed recovery exceeded the absolute ${DEADLINE_MS}ms deadline`);
    this.name = "ManagedRecoveryTimeoutError";
    this.code = "MANAGED_RECOVERY_TIMEOUT";
    this.timeoutEvidence = timeoutEvidence;
    this.cleanup = cleanup;
  }
}

export async function runDeadlineBoundOperation({ operation, deadline, captureTimeoutEvidence, cleanup, now = monotonicNow }) {
  if (!Number.isFinite(deadline)) throw new Error("deadline must be an absolute monotonic timestamp");
  let timer;
  let timedOut = false;
  const timeout = new Promise((_, reject) => {
    const remaining = Math.max(0, deadline - now());
    timer = setTimeout(() => {
      timedOut = true;
      void (async () => {
        let evidence;
        let cleanupResult;
        try { evidence = await captureTimeoutEvidence(); } catch (error) { evidence = { capture_error: error instanceof Error ? error.message : String(error) }; }
        try { cleanupResult = await cleanup(); } catch (error) { cleanupResult = { cleanup_error: error instanceof Error ? error.message : String(error) }; }
        reject(new ManagedRecoveryTimeoutError(evidence, cleanupResult));
      })();
    }, remaining);
  });
  const afterDeadline = () => timedOut || now() > deadline;
  const guarded = Promise.resolve(operation).then(
    (value) => afterDeadline() ? new Promise(() => {}) : value,
    (error) => afterDeadline() ? new Promise(() => {}) : Promise.reject(error),
  );
  try {
    return await Promise.race([guarded, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

export function serializeCleanup(cleanupOperation) {
  if (typeof cleanupOperation !== "function") throw new Error("cleanup operation must be a function");
  let cleanupPromise;
  return () => {
    if (cleanupPromise === undefined) cleanupPromise = Promise.resolve().then(cleanupOperation);
    return cleanupPromise;
  };
}

export function validateCompletion({ trial, restart_boundary, deadline, current_fence, lifecycle, persisted_receipt, provider_completion, transcript_completion }) {
  if (persisted_receipt === null || typeof persisted_receipt !== "object" || stable(Object.keys(persisted_receipt).sort()) !== stable(PERSISTED_RECEIPT_KEYS)) {
    throw new Error("router-persisted completion receipt schema is not exact");
  }
  if (persisted_receipt.event !== "managed_recovery_completion_persisted") throw new Error("completion receipt event is invalid");
  requireNonEmpty(persisted_receipt.receipt_id, "completion receipt_id");
  if (persisted_receipt.trial_id !== trial.trial_id || persisted_receipt.token !== trial.workload_token || persisted_receipt.agent_id !== trial.agent_id) throw new Error("completion receipt is not bound to the trial");
  if (persisted_receipt.runtime_generation !== current_fence.runtime_generation || persisted_receipt.process_incarnation !== current_fence.process_incarnation) throw new Error("completion receipt does not match the current fence");
  if (trial.pi_session_id !== current_fence.pi_session_id) throw new Error("replacement did not retain the exact pi_session_id");
  if (provider_completion?.completed !== true || provider_completion.active !== false || persisted_receipt.provider_request_seq !== provider_completion.sequence) throw new Error("completion receipt does not match provider completion");
  if (transcript_completion !== true) throw new Error("completion transcript evidence is unavailable");
  if (!Number.isFinite(persisted_receipt.observed_monotonic_ms)) throw new Error("completion receipt observed_monotonic_ms must be finite");
  if (persisted_receipt.observed_monotonic_ms <= restart_boundary) throw new Error("completion receipt must be after the restart boundary");
  if (persisted_receipt.observed_monotonic_ms > deadline) throw new Error("completion receipt must be no later than the absolute deadline");
  if (lifecycle?.lifecycle_state !== "running" || lifecycle.activity_state !== "idle" || lifecycle.accepting !== true) throw new Error("replacement lifecycle must be running, idle, and accepting prompts");
  return persisted_receipt;
}

export function validateBarrier(barrier, trial, currentFence) {
  if (barrier === null || typeof barrier !== "object" || stable(Object.keys(barrier).sort()) !== stable(PERSISTED_BARRIER_KEYS)) {
    throw new Error("router-persisted acknowledgement schema is not exact");
  }
  if (barrier.event !== "managed_recovery_in_progress" || barrier.acknowledged !== true || barrier.request_active !== true) throw new Error("in-progress barrier is not acknowledged and active");
  requireNonEmpty(barrier.acknowledgement_id, "barrier acknowledgement_id");
  if (barrier.trial_id !== trial.trial_id || barrier.token !== trial.workload_token || barrier.agent_id !== trial.agent_id) throw new Error("in-progress barrier is not bound to the trial");
  if (barrier.router_pid !== trial.router_pid) throw new Error("in-progress barrier is not bound to the isolated router PID");
  if (!Number.isSafeInteger(barrier.provider_request_seq) || barrier.provider_request_seq < 1) throw new Error("in-progress barrier provider sequence is invalid");
  if (!Number.isFinite(barrier.observed_monotonic_ms) || barrier.observed_monotonic_ms < 0) throw new Error("in-progress barrier observation time is invalid");
  if (stable(barrier.fence) !== stable(currentFence)) throw new Error("in-progress barrier DB fence is stale");
  return barrier;
}

export function validateFreshness({ pre_token, router_token, dom_token, restart_boundary, observed_monotonic_ms }) {
  if (!Number.isSafeInteger(pre_token) || !Number.isSafeInteger(router_token) || !Number.isSafeInteger(dom_token)) throw new Error("freshness tokens must be safe integers");
  if (observed_monotonic_ms <= restart_boundary) throw new Error("DOM observation must be post-boundary");
  if (dom_token <= pre_token) throw new Error("DOM freshness token did not change");
  if (dom_token !== router_token) throw new Error("DOM freshness token does not match the current router projection");
  return true;
}

function privateKeyPath(value, path = "publicEvidence") {
  if (Array.isArray(value)) {
    for (let index = 0; index < value.length; index++) {
      const found = privateKeyPath(value[index], `${path}[${index}]`);
      if (found) return found;
    }
    return undefined;
  }
  if (value === null || typeof value !== "object") return undefined;
  for (const [key, child] of Object.entries(value)) {
    if (PRIVATE_PUBLIC_KEY.test(key)) return `${path}.${key}`;
    const found = privateKeyPath(child, `${path}.${key}`);
    if (found) return found;
  }
  return undefined;
}

export function assertArtifactRedacted(publicEvidence, secrets) {
  const privatePath = privateKeyPath(publicEvidence);
  if (privatePath !== undefined) throw new Error(`public evidence contains private runtime field at ${privatePath}`);
  const encoded = stable(publicEvidence);
  for (const secret of secrets) {
    if ((typeof secret === "string" || typeof secret === "number") && String(secret).length > 0 && encoded.includes(String(secret))) {
      throw new Error("public evidence contains a private runtime identity");
    }
  }
  return true;
}

export function assertSharedResourcesUnchanged(evidence) {
  const snapshotKeys = ["configuration", "processes", "router", "user_tabs", "web_ui"];
  const invalidShape = () => new Error("shared-state evidence must contain captured before and after identities");
  if (evidence === null || typeof evidence !== "object" || stable(Object.keys(evidence).sort()) !== stable(["after", "before"])) {
    throw invalidShape();
  }
  for (const snapshot of [evidence.before, evidence.after]) {
    if (snapshot === null || typeof snapshot !== "object" || stable(Object.keys(snapshot).sort()) !== stable(snapshotKeys)) {
      throw invalidShape();
    }
    for (const field of snapshotKeys) {
      if (snapshot[field] === null || snapshot[field] === undefined || typeof snapshot[field] === "boolean") throw invalidShape();
    }
  }
  for (const field of snapshotKeys) {
    if (stable(evidence.before[field]) !== stable(evidence.after[field])) throw new Error(`shared ${field} identity changed`);
  }
  return evidence;
}

export function assertCleanupComplete(cleanup) {
  if (cleanup?.task_root_absent !== true || cleanup.profile_absent !== true || cleanup.processes_absent !== true || cleanup.ports_available !== true || cleanup.private_tabs_remaining !== 0) {
    throw new Error("isolated cleanup incomplete");
  }
  return cleanup;
}

export async function runManagedRecoverySequence({ trials, runTrial }) {
  if (trials !== 5 || typeof runTrial !== "function") throw new Error("managed recovery requires exactly five trials");
  const resourceKeys = ["bcb_bridge", "browser_profile", "browser_tab", "database", "router", "workload_token"];
  const seenResources = Object.fromEntries(resourceKeys.map((key) => [key, new Set()]));
  const results = [];
  for (let number = 1; number <= trials; number++) {
    const result = await runTrial(number);
    if (result?.result !== "passed") throw new Error(`managed recovery trial ${number} did not pass`);
    const resources = result.resources;
    if (resources === null || typeof resources !== "object" || Array.isArray(resources) || stable(Object.keys(resources).sort()) !== stable(resourceKeys)) {
      throw new Error(`managed recovery trial ${number} lacks fresh resource evidence: resources schema is not exact`);
    }
    for (const key of resourceKeys) {
      const value = resources[key];
      if (typeof value !== "string" || value.length === 0) {
        throw new Error(`managed recovery trial ${number} lacks fresh resource evidence: resources.${key} must be a non-empty string`);
      }
      if (seenResources[key].has(value)) throw new Error(`reused ${key}: resource value must be unique across trials`);
      seenResources[key].add(value);
    }
    results.push(result);
  }
  return results;
}
