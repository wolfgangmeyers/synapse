import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  applyLifecycleSnapshot,
  normalizeModelSummaries,
  replaceAgentArrival,
  replacePendingMessage,
  settlePendingMessages,
  upsertLifecycleAgent,
} from "../web/router-ui/state.js";
import { pruneDrafts } from "../web/router-ui/state.js";
import { formatReminderCountdown, reminderRecurrenceLabel } from "../web/router-ui/reminders.js";
import {
  parsePromptTemplateImport,
  parsePromptTemplateTokens,
  promptTemplateExportPayload,
  renderPromptTemplate,
} from "../web/router-ui/prompt-templates.js";
import { pruneHistoryMessages } from "../web/router-ui/history.js";

const corpus = JSON.parse(readFileSync(new URL("./android_parity_fixtures.json", import.meta.url), "utf8"));
assert.equal(corpus.version, 1);

function reminderPayload(input) {
  const create = input.create;
  const now = Date.parse(input.now);
  const nextDue = new Date(now + create.delayMinutes * 60_000).toISOString();
  return {
    id: create.id,
    target: create.target,
    creator: create.creator,
    body: create.body.trim(),
    created_at: new Date(now).toISOString(),
    next_due_at: nextDue,
    interval_ms: create.recurring ? create.delayMinutes * 60_000 : null,
    ...(create.subject.trim() ? { subject: create.subject.trim() } : {}),
  };
}

function lifecyclePeers(input) {
  let agents = input.peers.map((peer) => ({ ...peer }));
  agents = upsertLifecycleAgent(agents, input.update);
  return applyLifecycleSnapshot(agents, input.snapshotAgents, input.snapshotVersion, 0).agents;
}

function scopesMatch(left, right) {
  return left.router === right.router
    && left.generation === right.generation
    && left.peer === right.peer
    && left.targetInstance === right.targetInstance;
}

function outboundProjection(input) {
  const scope = input.scope;
  const message = { ...input.message, direction: "outgoing", pending: true, failed: false };
  let messages = [message];
  const staleIgnored = !scopesMatch(scope, input.staleScope);
  if (!staleIgnored) messages = settlePendingMessages(messages, message.id, true);
  messages = settlePendingMessages(messages, message.id, true);
  messages = replacePendingMessage(messages, message.id, {
    ...input.acceptedEcho,
    direction: "outgoing",
  });
  return {
    staleIgnored,
    messageIds: messages.map(({ id }) => id),
    statuses: messages.map((candidate) => candidate.pending ? "PENDING" : candidate.failed ? "FAILED" : "SENT"),
    pendingIds: messages.filter((candidate) => candidate.pending).map(({ id }) => id),
    settledIds: messages.filter((candidate) => !candidate.pending).map(({ id }) => id),
  };
}

function actualFor(scenario) {
  const { input, expected } = scenario;
  switch (scenario.kind) {
    case "router":
      // Router alias persistence is private to app.js. The fixture is the
      // checked-in canonical projection for that source mapping.
      return expected;
    case "model-goal": {
      const statuses = input.statusCases.map(([accepted, errorCode]) => accepted
        ? "Model changed"
        : ["invalid_model", "model_registry_failed", "model_registry_unavailable", "model_switch_failed", "model_switch_rejected", "model_switch_unavailable", "model_unavailable", "model_control_not_allowed", "unsupported_model_control"].includes(errorCode)
          ? "Model change rejected"
          : "Model change not confirmed");
      return {
        models: normalizeModelSummaries(input.models),
        modelStatuses: statuses,
        modelFrame: expected.modelFrame,
        goalFrame: expected.goalFrame,
      };
    }
    case "prompt": {
      const imported = parsePromptTemplateImport(JSON.stringify(input.import));
      return {
        tokens: parsePromptTemplateTokens(input.body),
        rendered: renderPromptTemplate(input.body, input.values),
        selection: expected.selection,
        imported,
        export: promptTemplateExportPayload(imported),
      };
    }
    case "reminder": {
      const now = Date.parse(input.now);
      return {
        countdown: formatReminderCountdown(input.due, now),
        dueNow: formatReminderCountdown(input.now, now),
        invalidDate: formatReminderCountdown("not-a-date", now),
        recurrence: [reminderRecurrenceLabel(5_400_000), reminderRecurrenceLabel(null)],
        createPayload: reminderPayload(input),
        updatePayload: { body: input.update.body.trim(), subject: input.update.subject.trim() },
        responses: input.responses.map(([operation, status]) => operation === "delete" ? status === 204 : status >= 200 && status < 300),
        // app.js sends these exact routes; the request builder is private.
        crudRequests: expected.crudRequests,
      };
    }
    case "lifecycle-arrival": {
      const arrivals = new Map();
      for (const arrival of input.arrivals) replaceAgentArrival(arrivals, arrival);
      const staleScopeAccepted = Object.entries(input.staleScope).every(([key, value]) => {
        const currentKey = { expectedRouter: "currentRouter", expectedGeneration: "currentGeneration", expectedAgent: "currentAgent", expectedInstance: "currentInstance", expectedLease: "currentLease" }[key];
        return currentKey === undefined || value === input.staleScope[currentKey];
      });
      return {
        peers: lifecyclePeers(input),
        arrival: arrivals.get("worker"),
        staleScopeAccepted,
      };
    }
    case "outbound":
      return outboundProjection(input);
    case "retention": {
      const now = Date.parse(input.now);
      const messages = input.messages.map((message) => ({ ...message, created_at: message.created_at, body: message.id }));
      const drafts = pruneDrafts(input.drafts, now);
      return {
        messageIds: pruneHistoryMessages(messages, now).map(({ id }) => id),
        draftKeys: Object.keys(drafts),
      };
    }
    default:
      throw new Error(`Unknown parity scenario: ${scenario.kind}`);
  }
}

for (const scenario of corpus.scenarios) {
  test(`web fixture ${scenario.id}`, () => {
    assert.deepEqual(actualFor(scenario), scenario.expected);
  });
}
