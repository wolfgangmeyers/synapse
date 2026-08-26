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

test("every reminder indicator is a muted empty-state button with keyboard activation", async () => {
  const app = await appSource();
  assert.match(app, /const empty = reminders\.length === 0;/);
  assert.equal(app.includes('class="reminder-indicator${empty ? " empty" : ""}'), true);
  assert.equal(app.includes('indicator.addEventListener("click", openReminder);'), true);
  assert.equal(app.includes('event.key !== "Enter" && event.key !== " "'), true);
  assert.equal(app.includes('openReminderDialog(peer, reminders.length === 0 ? "create" : "list", indicator)'), true);
});

test("WebUI reminder CRUD uses normal router POST and PATCH without lock actions", async () => {
  const app = await appSource();
  const server = await readFile(new URL("../tools/router_web_ui.mjs", import.meta.url), "utf8");
  assert.equal(app.includes('type: "reminder_crud"'), true);
  assert.equal(app.includes('sendReminderCrud(peer, "create"'), true);
  assert.equal(app.includes('interval_ms: recurring ? delayMinutes * 60_000 : null'), true);
  assert.equal(app.includes('sendReminderCrud(peer, "update"'), true);
  assert.equal(app.includes('{ body, subject: subject || null }'), true);
  assert.equal(app.includes('type: "reminder_crud", controlId'), false);
  assert.match(server, /methodByOperation = \{ create: "POST", update: "PATCH", delete: "DELETE" \}/);
  assert.match(server, /method === "POST"/);
  assert.equal(server.includes("/v1/reminders"), true);
});

test("reminder CRUD has pending feedback and disables duplicate edits", async () => {
  const app = await appSource();
  assert.equal(app.includes("pendingReminderCrud: new Map()"), true);
  assert.equal(app.includes('setReminderDialogStatus("Saving…")'), true);
  assert.equal(app.includes("reminderDialogStatus: null"), true);
  assert.equal(app.includes('status.id = "reminder-dialog-status"'), true);
  assert.equal(app.includes("save.disabled = [...state.pendingReminderCrud.values()].some"), true);
  assert.equal(app.includes("edit.disabled = pendingCrud"), true);
  assert.equal(app.includes("remove.disabled = pendingCrud"), true);
  assert.equal(app.includes("event.body?.error?.message || event.body?.error?.code"), true);
  assert.equal(app.includes("function handleReminderCrudResult(event)"), true);
});

test("reminder dialog owns one interval and clears it on close and router switch", async () => {
  const app = await appSource();
  assert.match(app, /reminderDialogTimer: null/);
  assert.match(app, /state\.reminderDialogTimer = setInterval\(/);
  assert.match(app, /elements\.remindersDialog\.addEventListener\("close", \(\) => \{[\s\S]*clearReminderDialogTimer\(\);/);
  assert.match(app, /async function switchRouter\(routerUrl, \{ connect = false \} = \{\}\) \{\n  closeReminderDialog\(\);/);
  assert.match(app, /function closeReminderDialog\(\) \{[\s\S]*clearReminderDialogTimer\(\);/);
});

test("reminder CRUD status is dialog-level and survives list redraws", async () => {
  const app = await appSource();
  assert.match(app, /function renderReminderDialogStatus\(\) \{[\s\S]*state\.reminderDialogStatus/);
  assert.match(app, /function clearReminderDialogStatus\(\) \{[\s\S]*state\.reminderDialogStatus = null/);
  assert.match(app, /setReminderDialogStatus\(event\.body\?\.error\?\.message[\s\S]*renderReminderDialog\(pending\.peer\)/);
  assert.match(app, /elements\.remindersDialog\.addEventListener\("close",[\s\S]*clearReminderDialogStatus\(\)/);
  assert.match(app, /function sendReminderCrud\([\s\S]*clearReminderDialogStatus\(\)/);
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
