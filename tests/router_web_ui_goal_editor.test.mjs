import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);
const read = (path) => readFile(new URL(path, root), "utf8");

test("goal editor markup has one shared dialog and an icon-only header entry point", async () => {
  const html = await read("web/router-ui/index.html");
  const css = await read("web/router-ui/styles.css");
  assert.equal((html.match(/id="goal-dialog"/g) || []).length, 1);
  assert.match(html, /id="goal-header-button"[^>]*type="button"[^>]*aria-controls="goal-dialog"/);
  assert.match(html, /id="goal-dialog-body"/);
  assert.match(css, /\.goal-header-button\s*\{[\s\S]*font-size: 0;/);
  assert.match(css, /\.goal-header-button i\s*\{[\s\S]*font-size: 11px;/);
});

test("goal editor keeps the scrollable form separate from its pinned footer actions", async () => {
  const html = await read("web/router-ui/index.html");
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  assert.match(html, /id="goal-dialog-body"[^>]*class="goal-dialog-body"/);
  assert.match(html, /id="goal-dialog-footer"[^>]*class="goal-dialog-footer"/);
  assert.match(app, /goalDialogFooter: document\.querySelector\("#goal-dialog-footer"\)/);
  assert.match(app, /elements\.goalDialogBody\.append\(form\);[\s\S]*elements\.goalDialogFooter\.append\(status, actions\)/);
  assert.match(app, /elements\.goalDialogFooter\.querySelector\("#goal-editor-status"\)/);
  assert.match(css, /#goal-dialog\s*\{[\s\S]*max-height:\s*calc\(100dvh - 32px\);/);
  assert.match(css, /#goal-dialog-body\s*\{[\s\S]*min-height:\s*0;[\s\S]*overflow-y:\s*auto;/);
  assert.match(css, /\.goal-editor-dialog \.dialog-shell\s*\{[\s\S]*display:\s*grid;[\s\S]*grid-template-rows:\s*auto minmax\(0, 1fr\) auto;[\s\S]*max-height:\s*calc\(100dvh - 32px\);/);
  assert.match(css, /\.goal-dialog-footer\s*\{[\s\S]*border-top:\s*1px solid var\(--line\);[\s\S]*background:\s*var\(--surface\);/);
});

test("goal editor Cancel closes locally without sending or changing confirmed state", async () => {
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  assert.match(app, /const cancel = document\.createElement\("button"\);[\s\S]*cancel\.textContent = "Cancel";[\s\S]*cancel\.addEventListener\("click", closeGoalDialog\)/);
  assert.doesNotMatch(app, /Turn off/);
  assert.doesNotMatch(app, /sendGoalControl\("off"\)/);
  assert.doesNotMatch(css, /danger-action/);
  assert.match(app, /elements\.goalDialog\.addEventListener\("close", \(\) => \{[\s\S]*state\.goalDraft = null/);
  assert.match(app, /cancel\.addEventListener\("click", closeGoalDialog\)/);
});

test("goal editor saves an unchecked Goal on draft as a typed off control", async () => {
  const app = await read("web/router-ui/app.js");
  assert.match(app, /save\.addEventListener\("click", \(\) => sendGoalControl\(\)\)/);
  assert.match(app, /function sendGoalControl\(\)[\s\S]*const action = draft\?\.enabled \? "set" : "off";/);
  assert.doesNotMatch(app, /if \(action === "set" && \(!draft\.enabled/);
  assert.match(app, /action === "off" \? "" : draft\.goal/);
  assert.match(app, /if \(event\.accepted === true && event\.goalState\) updateAgentGoalState\(event\.targetAgentId, event\.targetInstanceId, event\.goalState\)/);
});

test("goal editor has a small left-aligned muted or pink flag with one shared opening path", async () => {
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  const headerButtonCss = css.match(/\.goal-header-button\s*\{([\s\S]*?)\n\}/)?.[1] || "";
  assert.match(app, /elements\.goalHeaderButton\.addEventListener\("click", \(\) => \{[\s\S]*openGoalEditor\(state\.selectedPeer, elements\.goalHeaderButton\)/);
  assert.match(app, /goalHeaderButton\.classList\.toggle\("has-goal", hasGoal\)/);
  assert.match(app, /goalHeaderButton\.innerHTML = '<i class="fa-solid fa-flag" aria-hidden="true"><\/i>'/);
  assert.match(app, /const goalLabel = hasGoal \? `Edit goal for \$\{humanName\(peer\)\}` : `Set goal for \$\{humanName\(peer\)\}`/);
  assert.match(app, /goalHeaderButton\.setAttribute\("aria-label", peer \? goalLabel/);
  assert.match(app, /goalHeaderButton\.title = peer \? goalLabel/);
  assert.match(headerButtonCss, /flex: 0 0 24px/);
  assert.match(headerButtonCss, /width: 24px/);
  assert.match(headerButtonCss, /margin: 0/);
  assert.doesNotMatch(headerButtonCss, /margin-left:\s*auto/);
  assert.match(css, /\.goal-header-button\.has-goal[\s\S]*background: rgba\(190,24,93,0\.34\)[\s\S]*color: #f9a8d4/);
  assert.match(css, /\.goal-header-button\.has-goal i\s*\{\s*color: #f472b6/);
  assert.match(app, /function attachGoalActions\(root, goal\)[\s\S]*openGoalEditor\(root\.dataset\.agentId, indicator\)/);
  assert.match(app, /state\.goalDraft = goalDraftFromState\(source\)/);
  assert.match(app, /state\.goalDraft = null/);
  assert.match(app, /goalText\.textContent|goalText\.value = draft\.goal/);
});

test("header keeps the goal flag in the identity row above the subagent badge", async () => {
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  const renderConversation = app.slice(app.indexOf("function renderConversation()"), app.indexOf("function renderConversation()") + 3000);
  assert.match(renderConversation, /elements\.peerName\.innerHTML[\s\S]*elements\.peerName\.append\(elements\.goalHeaderButton\)/);
  assert.match(renderConversation, /elements\.peerName\.append\(elements\.goalHeaderButton\)[\s\S]*elements\.peerDetail\.className/);
  assert.match(renderConversation, /elements\.peerDetail\.innerHTML = peer \? \[[\s\S]*subagentBadgeMarkup\(peer, subagentCount, subagents, expanded, "header"\)/);
  assert.match(css, /#peer-name\s*\{\s*display:\s*flex;[\s\S]*align-items:\s*center;/);
  assert.match(css, /\.peer-activity\s*\{[\s\S]*flex-wrap:\s*wrap;/);
  assert.doesNotMatch(css, /\.goal-header-button[^}]*position:\s*(?:absolute|fixed)/);
});

test("goal editor uses a roomy plain Markdown textarea without changing draft value handling", async () => {
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  const editorCss = css.match(/\.goal-editor-form textarea\s*\{([\s\S]*?)\n\}/)?.[1] || "";
  assert.match(app, /const goalText = document\.createElement\("textarea"\)/);
  assert.match(app, /goalText\.rows = 10;/);
  assert.match(app, /goalText\.value = draft\.goal;/);
  assert.match(editorCss, /min-height:\s*240px/);
  assert.match(editorCss, /resize:\s*vertical/);
  assert.match(editorCss, /line-height:\s*1\.55/);
  assert.match(editorCss, /font-family:\s*ui-monospace/);
  assert.match(editorCss, /white-space:\s*pre-wrap/);
  assert.match(editorCss, /overflow-wrap:\s*anywhere/);
  assert.match(editorCss, /background:\s*var\(--surface-raised\)/);
  assert.match(css, /\.goal-editor-form textarea:focus\s*\{[\s\S]*border-color:\s*var\(--secondary\)/);
  assert.match(css, /@media \(max-width: 820px\)[\s\S]*\.goal-editor-form textarea\s*\{[\s\S]*min-height:\s*200px[\s\S]*max-height:\s*50vh/);
});

test("goal editor uses accessible ON/OFF switches and a lock state button", async () => {
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  assert.match(app, /function goalToggle\(id, labelText, pressed, \{ lock = false \} = \{\}\)/);
  assert.match(app, /button\.type = "button";/);
  assert.match(app, /button\.setAttribute\("aria-pressed", String\(pressed\)\)/);
  assert.match(app, /goalToggle\("goal-enabled", "Goal", draft\.enabled\)/);
  assert.match(app, /goalToggle\("goal-lock", "Goal lock", draft\.goal_lock_enabled, \{ lock: true \}\)/);
  assert.match(app, /goalToggle\("snooze-enabled", "Allow snoozing", draft\.snooze_enabled\)/);
  assert.match(app, /if \(state\) state\.textContent = lock \? \(pressed \? "LOCKED" : "UNLOCKED"\) : \(pressed \? "ON" : "OFF"\)/);
  assert.match(app, /track\.append\(thumb\);[\s\S]*thumb\.append\(status\);/);
  assert.match(app, /if \(lock\) status\.classList\.add\("goal-toggle-state-hidden"\);/);
  assert.match(app, /if \(lock\) thumb\.append\(icon, status\);[\s\S]*button\.append\(label, track\);/);
  assert.match(app, /fa-lock-open/);
  assert.doesNotMatch(app, /goalCheckbox|input\.type = "checkbox"/);
  assert.match(css, /\.goal-toggle-track[\s\S]*width: 95px[\s\S]*height: 30px[\s\S]*background: #101623/);
  assert.match(css, /\.goal-toggle[\s\S]*min-height: 56px[\s\S]*padding: 12px/);
  assert.match(css, /\.goal-toggle-thumb[\s\S]*width: calc\(50% - 3px\)[\s\S]*height: 26px/);
  assert.match(css, /\.goal-toggle-label \{ min-width: 0; font-weight: 800; \}/);
  assert.match(css, /\.goal-toggle-thumb \.goal-toggle-state[\s\S]*font-size: 9px[\s\S]*font-weight: 800[\s\S]*text-align: center/);
  assert.match(css, /\.goal-toggle-state-hidden[\s\S]*position: absolute[\s\S]*clip: rect\(0, 0, 0, 0\)/);
  assert.match(css, /\.goal-toggle\[aria-pressed="true"\] \.goal-toggle-thumb[\s\S]*transform: translateX\(100%\)/);
  assert.match(css, /\.goal-toggle\[aria-pressed="true"\] \.goal-toggle-thumb \.goal-toggle-state[\s\S]*text-shadow/);
  assert.match(css, /\.goal-lock-toggle \.goal-toggle-thumb i\s*\{[^}]*color:\s*#101623[^}]*font-size:\s*9px[^}]*\}/);
  assert.doesNotMatch(css, /\.goal-lock-toggle \.goal-toggle-thumb \.goal-toggle-state[\s\S]*transform: scaleX/);
  assert.match(css, /\.goal-lock-toggle\[aria-pressed="true"\] \.goal-toggle-thumb[\s\S]*background: var\(--primary\)/);
});

test("goal editor sends complete typed goal controls and rejects stale target results", async () => {
  const app = await read("web/router-ui/app.js");
  const state = await read("web/router-ui/state.js");
  const relay = await read("tools/router_web_ui.mjs");
  assert.match(app, /type: "goal_control"/);
  for (const key of [
    "goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit",
    "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms",
  ]) assert.match(app, new RegExp(key));
  assert.match(app, /action === "off" \? "" : draft\.goal/);
  assert.match(state, /"agent_goal_state"/);
  assert.match(state, /"goal_control_result"/);
  assert.match(relay, /type: "goal_control"/);
  assert.match(relay, /target_instance_id: targetInstanceId/);
  assert.match(relay, /pending\.targetInstanceId !== frame\.instance_id/);
  assert.match(relay, /frame\.source_agent_id !== configured\.agentId/);
  assert.match(relay, /agent_role: "user_interaction"/);
  assert.doesNotMatch(app.slice(app.indexOf('type: "goal_control"'), app.indexOf('type: "goal_control"') + 900), /model_control/);
});
