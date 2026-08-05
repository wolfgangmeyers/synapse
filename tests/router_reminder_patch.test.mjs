import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository, ReminderValidationError } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const NOW = "2030-01-01T00:05:00.000Z";
const HEADERS = { "content-type": "application/json" };

async function withRouter(t, clock = () => new Date(NOW)) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-reminder-patch-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({
    databasePath: join(directory, "router.sqlite"),
    clock,
  });
  t.after(() => repository.close());
  const router = createAgentRouter({ repository, clock });
  t.after(async () => {
    try {
      await router.close();
    } catch {
      // Tests that never listen have no server to close.
    }
  });
  return { repository, router };
}

function baseReminder(overrides = {}) {
  return {
    id: "patch-target",
    target: "supervisor",
    creator: "supervisor",
    body: "original body",
    created_at: "2030-01-01T00:00:00.000Z",
    // Far enough in the future that no sweep fires during the test.
    next_due_at: "2030-01-01T01:00:00.000Z",
    interval_ms: 10 * 60 * 1_000,
    ...overrides,
  };
}

test("updateReminder recomputes next_due_at when the interval changes", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder());

  const result = repository.updateReminder("patch-target", { interval_ms: 5 * 60 * 1_000 });

  assert.equal(result.schedulingChanged, true);
  const reminder = result.reminder;
  assert.equal(reminder.interval_ms, 5 * 60 * 1_000);
  // now (00:05) + 5 minutes.
  assert.equal(reminder.next_due_at, "2030-01-01T00:10:00.000Z");
  // A re-armed schedule clears retry accounting.
  assert.equal(reminder.attempt_count, 0);
  assert.equal(reminder.last_attempt_at, null);
});

test("updateReminder preserves immutable fields and occurrence_number", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder());
  const before = repository.reminderFor("patch-target");

  repository.updateReminder("patch-target", { interval_ms: 5 * 60 * 1_000 });

  const after = repository.reminderFor("patch-target");
  assert.equal(after.id, before.id);
  assert.equal(after.created_at, before.created_at);
  assert.equal(after.creator, before.creator);
  assert.equal(after.occurrence_number, before.occurrence_number);
});

test("updateReminder changes only body and subject and leaves the schedule untouched", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder());

  const result = repository.updateReminder("patch-target", {
    body: "new body",
    subject: "new subject",
  });

  assert.equal(result.schedulingChanged, false);
  const reminder = result.reminder;
  assert.equal(reminder.body, "new body");
  assert.equal(reminder.subject, "new subject");
  // Schedule fields are untouched when only content changes.
  assert.equal(reminder.interval_ms, 10 * 60 * 1_000);
  assert.equal(reminder.next_due_at, "2030-01-01T01:00:00.000Z");
});

test("updateReminder clears the subject when patched to null", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder({ subject: "has subject" }));

  const reminder = repository.updateReminder("patch-target", { subject: null }).reminder;

  assert.equal(reminder.subject, undefined);
});

test("updateReminder returns undefined for an unknown reminder id", async (t) => {
  const { repository } = await withRouter(t);
  assert.equal(repository.updateReminder("missing", { body: "x" }), undefined);
});

test("updateReminder rejects unknown patch keys and empty patches", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder());
  assert.throws(() => repository.updateReminder("patch-target", { state: "scheduled" }), ReminderValidationError);
  assert.throws(() => repository.updateReminder("patch-target", { surprise: 1 }), ReminderValidationError);
  assert.throws(() => repository.updateReminder("patch-target", {}), ReminderValidationError);
});

test("updateReminder rejects an invalid interval_ms", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder());
  for (const intervalMs of [0, -5, 1.5, "10"]) {
    assert.throws(
      () => repository.updateReminder("patch-target", { interval_ms: intervalMs }),
      ReminderValidationError,
    );
  }
});

test("updateReminder respects an explicit next_due_at over the interval recompute", async (t) => {
  const { repository } = await withRouter(t);
  repository.createReminder(baseReminder());

  const reminder = repository.updateReminder("patch-target", {
    interval_ms: 5 * 60 * 1_000,
    next_due_at: "2030-02-01T00:00:00.000Z",
  }).reminder;

  assert.equal(reminder.interval_ms, 5 * 60 * 1_000);
  assert.equal(reminder.next_due_at, "2030-02-01T00:00:00.000Z");
});

test("PATCH /v1/reminders/:id updates the interval and returns the reminder", async (t) => {
  const { repository, router } = await withRouter(t);
  repository.createReminder(baseReminder());
  const url = new URL(await router.listen());
  url.protocol = "http:";

  const response = await fetch(`${url.origin}/v1/reminders/patch-target`, {
    method: "PATCH",
    headers: HEADERS,
    body: JSON.stringify({ interval_ms: 5 * 60 * 1_000 }),
  });

  assert.equal(response.status, 200);
  const payload = await response.json();
  const reminder = payload.reminder;
  assert.equal(reminder.id, "patch-target");
  assert.equal(reminder.interval_ms, 5 * 60 * 1_000);
  assert.equal(reminder.next_due_at, "2030-01-01T00:10:00.000Z");
});

test("PATCH /v1/reminders/:id returns 404 for an unknown reminder", async (t) => {
  const { router } = await withRouter(t);
  const url = new URL(await router.listen());
  url.protocol = "http:";

  const response = await fetch(`${url.origin}/v1/reminders/missing`, {
    method: "PATCH",
    headers: HEADERS,
    body: JSON.stringify({ body: "x" }),
  });

  assert.equal(response.status, 404);
});

test("PATCH /v1/reminders/:id rejects an invalid interval with 400", async (t) => {
  const { repository, router } = await withRouter(t);
  repository.createReminder(baseReminder());
  const url = new URL(await router.listen());
  url.protocol = "http:";

  const response = await fetch(`${url.origin}/v1/reminders/patch-target`, {
    method: "PATCH",
    headers: HEADERS,
    body: JSON.stringify({ interval_ms: 0 }),
  });

  assert.equal(response.status, 400);
  const payload = await response.json();
  assert.equal(payload.error.code, "invalid_reminder");
});

test("PATCH /v1/reminders/:id rejects a non-JSON content type", async (t) => {
  const { repository, router } = await withRouter(t);
  repository.createReminder(baseReminder());
  const url = new URL(await router.listen());
  url.protocol = "http:";

  const response = await fetch(`${url.origin}/v1/reminders/patch-target`, {
    method: "PATCH",
    headers: { "content-type": "text/plain" },
    body: "plain",
  });

  assert.equal(response.status, 400);
});
