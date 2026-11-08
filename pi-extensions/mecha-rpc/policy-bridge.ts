import type { JsonRecord } from "./manifest.js";

export type ManagedGoalControlProjection = {
  action: "set" | "off";
  controlId: string;
  targetInstanceId: string;
  revision: number;
  goal?: string;
  settings: JsonRecord;
};

export type ManagedPolicyProjection = {
  inlineEnabled: boolean;
  inlineCredits: Readonly<Record<string, number>>;
  recallContent: string;
  settledEnabled: boolean;
  compactionDeferred: boolean;
  goalControl?: ManagedGoalControlProjection;
};

const EMPTY_POLICY: ManagedPolicyProjection = {
  inlineEnabled: false,
  inlineCredits: {},
  recallContent: "",
  settledEnabled: false,
  compactionDeferred: false,
};

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function boundedBoolean(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function validGoalSettings(settings: JsonRecord): boolean {
  const keys = ["snoozeEnabled", "snoozeDurationMs", "snoozeTurnLimit", "snoozeBudgetCapMs", "snoozeRefillYieldMs", "snoozeRefillElapsedMs"];
  const positiveInteger = (value: unknown) => typeof value === "number" && Number.isSafeInteger(value) && value > 0;
  const optionalPositiveInteger = (value: unknown) => value === null || positiveInteger(value);
  return Object.keys(settings).length === keys.length && keys.every((key) => Object.hasOwn(settings, key))
    && typeof settings.snoozeEnabled === "boolean"
    && positiveInteger(settings.snoozeDurationMs)
    && optionalPositiveInteger(settings.snoozeTurnLimit)
    && optionalPositiveInteger(settings.snoozeBudgetCapMs)
    && positiveInteger(settings.snoozeRefillYieldMs)
    && positiveInteger(settings.snoozeRefillElapsedMs)
    && (settings.snoozeTurnLimit === null || (typeof settings.snoozeTurnLimit === "number" && settings.snoozeTurnLimit <= 20));
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
      const allowed = new Set(["inline", "recall", "settled", "compaction", "goal"]);
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
      const compaction = value.compaction === undefined ? {} : closedRecord(value.compaction, ["deferred"], "compaction");
      let goalControl: ManagedGoalControlProjection | undefined;
      if (value.goal !== undefined) {
        const goal = closedRecord(value.goal, ["action", "control_id", "target_instance_id", "revision", "goal", "settings"], "goal");
        const action = goal.action;
        const controlId = goal.control_id;
        const targetInstanceId = goal.target_instance_id;
        const revision = goal.revision;
        const settings = goal.settings;
        if ((action !== "set" && action !== "off") || typeof controlId !== "string" || controlId.length === 0
          || typeof targetInstanceId !== "string" || targetInstanceId.length === 0
          || typeof revision !== "number" || !Number.isSafeInteger(revision) || revision < 1
          || !isRecord(settings) || !validGoalSettings(settings)) throw new TypeError("Managed goal projection is invalid");
        const goalText = typeof goal.goal === "string" ? goal.goal : undefined;
        if (action === "set" && (goalText === undefined || goalText.trim().length === 0 || Buffer.byteLength(goalText, "utf8") > 4_000)) {
          throw new TypeError("Managed goal projection is invalid");
        }
        if (action === "off" && goal.goal !== undefined) throw new TypeError("Managed goal projection is invalid");
        goalControl = { action, controlId, targetInstanceId, revision, ...(goalText === undefined ? {} : { goal: goalText }), settings: structuredClone(settings) };
      }
      policy = Object.freeze({
        inlineEnabled: boundedBoolean(inline.enabled, false),
        inlineCredits: Object.freeze(credits),
        recallContent: content,
        settledEnabled: boundedBoolean(settled.enabled, false),
        compactionDeferred: boundedBoolean(compaction.deferred, false),
        ...(goalControl === undefined ? {} : { goalControl }),
      });
      return policy;
    },
    snapshot(): ManagedPolicyProjection {
      return policy;
    },
    consumeInlineCredit(toolName: string): boolean {
      if (!["bash", "edit", "write"].includes(toolName)) return false;
      // Match mecha-messaging: the durable UI setting is a broad runtime
      // permission. Credits are only relevant while that setting is off.
      if (policy.inlineEnabled) return true;
      const count = policy.inlineCredits[toolName] ?? 0;
      if (count <= 0) return false;
      const credits = { ...policy.inlineCredits, [toolName]: count - 1 };
      policy = Object.freeze({ ...policy, inlineCredits: Object.freeze(credits) });
      return true;
    },
  };
}
