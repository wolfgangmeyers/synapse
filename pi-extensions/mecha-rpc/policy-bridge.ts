import type { JsonRecord } from "./manifest.js";

export type ManagedPolicyProjection = {
  inlineEnabled: boolean;
  inlineCredits: Readonly<Record<string, number>>;
  recallContent: string;
  settledEnabled: boolean;
  goalLockEnabled: boolean;
  compactionDeferred: boolean;
};

const EMPTY_POLICY: ManagedPolicyProjection = {
  inlineEnabled: false,
  inlineCredits: {},
  recallContent: "",
  settledEnabled: false,
  goalLockEnabled: false,
  compactionDeferred: false,
};

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function boundedBoolean(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function closedRecord(value: unknown, allowed: readonly string[], label: string): JsonRecord {
  if (!isRecord(value) || Object.keys(value).some((key) => !allowed.includes(key))) {
    throw new TypeError(`Managed ${label} projection is invalid`);
  }
  return value;
}

/** Local mechanical projection only. Router state remains authoritative. */
export function createPolicyBridge() {
  let policy: ManagedPolicyProjection = EMPTY_POLICY;

  return {
    apply(value: unknown): ManagedPolicyProjection {
      if (!isRecord(value)) throw new TypeError("Managed policy projection must be an object");
      const allowed = new Set(["inline", "recall", "settled", "goal_lock", "compaction"]);
      if (Object.keys(value).some((key) => !allowed.has(key))) throw new TypeError("Managed policy projection has unsupported fields");
      const inline = value.inline === undefined ? {} : closedRecord(value.inline, ["enabled", "credits"], "inline");
      const creditsRaw = inline.credits === undefined ? {} : closedRecord(inline.credits, ["bash", "edit", "write"], "inline credits");
      const credits: Record<string, number> = {};
      for (const [name, count] of Object.entries(creditsRaw)) {
        if (!/^[a-z_]{1,64}$/.test(name) || typeof count !== "number" || !Number.isSafeInteger(count) || count < 0 || count > 5) {
          throw new TypeError("Managed inline credits are invalid");
        }
        credits[name] = count;
      }
      const recall = value.recall === undefined ? {} : closedRecord(value.recall, ["content"], "recall");
      const content = typeof recall.content === "string" ? recall.content : "";
      if (Buffer.byteLength(content, "utf8") > 24_000) throw new TypeError("Managed recall projection is too large");
      const settled = value.settled === undefined ? {} : closedRecord(value.settled, ["enabled"], "settled");
      const goalLock = value.goal_lock === undefined ? {} : closedRecord(value.goal_lock, ["enabled"], "goal lock");
      const compaction = value.compaction === undefined ? {} : closedRecord(value.compaction, ["deferred"], "compaction");
      policy = Object.freeze({
        inlineEnabled: boundedBoolean(inline.enabled, false),
        inlineCredits: Object.freeze(credits),
        recallContent: content,
        settledEnabled: boundedBoolean(settled.enabled, false),
        goalLockEnabled: boundedBoolean(goalLock.enabled, false),
        compactionDeferred: boundedBoolean(compaction.deferred, false),
      });
      return policy;
    },
    snapshot(): ManagedPolicyProjection {
      return policy;
    },
    consumeInlineCredit(toolName: string): boolean {
      if (!policy.inlineEnabled || !["bash", "edit", "write"].includes(toolName)) return false;
      const count = policy.inlineCredits[toolName] ?? 0;
      if (count <= 0) return false;
      const credits = { ...policy.inlineCredits, [toolName]: count - 1 };
      policy = Object.freeze({ ...policy, inlineCredits: Object.freeze(credits) });
      return true;
    },
  };
}
