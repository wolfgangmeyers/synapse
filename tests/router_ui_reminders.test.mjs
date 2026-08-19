import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { formatReminderCountdown, reminderRecurrenceLabel } from "../web/router-ui/reminders.js";

const appSource = () => readFile(new URL("../web/router-ui/app.js", import.meta.url), "utf8");

test("formats reminder countdowns without exposing raw timestamps", () => {
  const dueAt = Date.parse("2030-01-02T03:04:05.000Z");
  assert.equal(formatReminderCountdown(dueAt, dueAt - (2 * 24 * 60 * 60 * 1000 + 3 * 60 * 60 * 1000 + 4 * 60 * 1000 + 5 * 1000)), "Due in 2d 3h 4m 5s");
  assert.equal(formatReminderCountdown(dueAt, dueAt), "Due now");
  assert.equal(formatReminderCountdown("not-a-date", dueAt), "Due time unavailable");
});

test("labels reminders from their existing interval", () => {
  assert.equal(reminderRecurrenceLabel(60_000), "Recurring");
  assert.equal(reminderRecurrenceLabel(null), "One-shot");
  assert.equal(reminderRecurrenceLabel(0), "One-shot");
});

test("reminder dialog markup uses countdown and recurrence labels", async () => {
  const app = await appSource();
  assert.match(app, /formatReminderCountdown\(reminder\.next_due_at\)/);
  assert.match(app, /reminderRecurrenceLabel\(reminder\.interval_ms\)/);
  assert.match(app, /class="reminder-countdown"/);
  assert.match(app, /class="reminder-recurrence"/);
  assert.doesNotMatch(app, /<small>\$\{escapeHtml\(reminder\.next_due_at \|\| ""\)\}<\/small>/);
});

test("reminder indicator keyboard activation opens its dialog without selecting the row", async () => {
  const app = await appSource();
  assert.match(app, /indicator\.addEventListener\("click", openReminder\);/);
  assert.match(app, /indicator\.addEventListener\("keydown", \(event\) => \{\n    if \(event\.key !== "Enter" && event\.key !== " "\) return;\n    event\.preventDefault\(\);\n    event\.stopPropagation\(\);\n    openReminder\(event\);\n  \}\);/);
});

test("reminder dialog owns one interval and clears it on close and router switch", async () => {
  const app = await appSource();
  assert.match(app, /reminderDialogTimer: null/);
  assert.match(app, /state\.reminderDialogTimer = setInterval\(/);
  assert.match(app, /elements\.remindersDialog\.addEventListener\("close", \(\) => \{[\s\S]*clearReminderDialogTimer\(\);/);
  assert.match(app, /async function switchRouter\(routerUrl, \{ connect = false \} = \{\}\) \{\n  closeReminderDialog\(\);/);
  assert.match(app, /function closeReminderDialog\(\) \{[\s\S]*clearReminderDialogTimer\(\);/);
});

test("reminder dialog redraws from current active reminders after a state transition", async () => {
  const app = await appSource();
  assert.match(app, /renderReminderDialog\(state\.reminderDialogPeer\);/);
  assert.match(app, /renderReminderDialog\(peer\);\n    \}, 1000\);/);
  assert.match(app, /function renderReminderDialog\(peer\) \{[\s\S]*state\.reminders\.filter\([\s\S]*\["scheduled", "due", "message_queued"\]/);
  assert.doesNotMatch(app, /renderReminderDialog\([^)]*, reminders\)/);
  assert.doesNotMatch(app, /const visible = reminders\.length > 0 \? reminders : fallback/);
});

test("peer and agent scope changes close the reminder dialog", async () => {
  const app = await appSource();
  assert.match(app, /function selectPeer\(peer\) \{\n  closeReminderDialog\(\);/);
  assert.match(app, /async function switchRouter\(routerUrl, \{ connect = false \} = \{\}\) \{\n  closeReminderDialog\(\);/);
});
