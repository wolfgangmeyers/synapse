import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { test } from "node:test";
import { parseRequest, parseResponse, projectCatalog, promptEvent, terminalResult } from "./protocol.mjs";

async function runHelper(input, env = {}) {
  const child = spawn(process.execPath, [join(import.meta.dirname, "index.mjs")], {
    env: { PATH: process.env.PATH, HOME: env.HOME, MECHA_MANAGED_AUTH_PATH: env.authPath, MECHA_TEST_SENTINEL: "do-not-leak" },
    stdio: ["pipe", "pipe", "pipe"],
  });
  child.stdin.end(input);
  let stdout = "";
  let stderr = "";
  for await (const chunk of child.stdout) stdout += chunk;
  for await (const chunk of child.stderr) stderr += chunk;
  const exitCode = await new Promise((resolve) => child.once("close", resolve));
  return { exitCode, stdout, stderr };
}

test("package root and required Cursor deep imports resolve from the lockfile", async () => {
  const packageJson = JSON.parse(await readFile(new URL("./package.json", import.meta.url), "utf8"));
  assert.deepEqual(packageJson.dependencies, {
    "@earendil-works/pi-coding-agent": "0.85.0",
    "@earendil-works/pi-ai": "0.85.0",
    "@earendil-works/pi-server": "0.85.0",
    "pi-cursor-sdk": "0.3.6",
  });
  assert.match(await import.meta.resolve("@earendil-works/pi-coding-agent"), /pi-coding-agent/);
  assert.match(await import.meta.resolve("pi-cursor-sdk/dist/index.js"), /pi-cursor-sdk/);
  assert.match(await import.meta.resolve("pi-cursor-sdk/dist/model-discovery.js"), /pi-cursor-sdk/);
});

test("catalog projection is deterministic, deduplicated, and excludes unavailable models", () => {
  assert.deepEqual(projectCatalog([
    { provider: "cursor", id: "two", name: "Two", available: true },
    { provider: "google", id: "one", name: "One", available: true },
    { provider: "google", id: "one", name: "duplicate", available: true },
    { provider: "google", id: "secret", name: "Secret", available: false },
  ]), {
    models: [
      { provider: "cursor", id: "two", name: "Two", available: true },
      { provider: "google", id: "one", name: "One", available: true },
    ],
  });
});

test("an unsupported OpenAI API-key credential is never treated as ChatGPT subscription auth", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, JSON.stringify({ openai: { type: "api_key", key: "synthetic-test-key" } }), { mode: 0o600 });
  const result = await runHelper('{"operation":"models"}\n', { HOME: home, authPath });
  assert.equal(result.exitCode, 0);
  const catalog = JSON.parse(result.stdout);
  assert.equal(catalog.models.some((model) => model.provider === "openai"), false);
  assert.equal(result.stdout.includes("synthetic-test-key"), false);
});

test("provider auth protocol binds each prompt and terminal snapshot to one mapped provider", () => {
  const request = parseRequest('{"operation":"login","operation_id":"operation-1","provider":"google"}');
  assert.deepEqual(parseResponse('{"type":"response","prompt_id":"prompt-1","value":"synthetic-key"}'), { type: "response", prompt_id: "prompt-1", value: "synthetic-key" });
  assert.deepEqual(promptEvent(request, { id: "prompt-1", kind: "secret", message: "Gemini API key" }), {
    type: "prompt", operation_id: "operation-1", provider: "google", prompt: { id: "prompt-1", kind: "secret", message: "Gemini API key" },
  });
  assert.deepEqual(promptEvent(request, { id: "prompt-2", kind: "manual_code", message: "Open the sign-in page", url: "https://accounts.google.com/device" }), {
    type: "prompt", operation_id: "operation-1", provider: "google", prompt: { id: "prompt-2", kind: "manual_code", message: "Open the sign-in page", url: "https://accounts.google.com/device" },
  });
  const terminal = terminalResult(request, "succeeded", {
    providers: [
      { provider: "openai-codex", label: "ChatGPT", authenticated: false, status: "signed_out" },
      { provider: "google", label: "Google", authenticated: true, status: "authenticated" },
      { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out" },
    ],
    models: [{ provider: "google", id: "gemini-test", name: "Gemini test", available: true }],
  });
  assert.equal(terminal.operation_id, "operation-1");
  assert.equal(terminal.provider, "google");
  assert.equal(JSON.stringify(terminal).includes("synthetic-key"), false);
  assert.throws(() => parseRequest('{"operation":"login","operation_id":"operation-1","provider":"openai"}'));
  assert.throws(() => parseResponse('{"type":"response","prompt_id":"prompt-1","value":"x","extra":true}'));
});

test("a protected Cursor credential activates the pinned Cursor catalog adapter", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, JSON.stringify({ cursor: { type: "api_key", key: "synthetic-cursor-key" } }), { mode: 0o600 });
  const result = await runHelper('{"operation":"models"}\n', { HOME: home, authPath });
  assert.equal(result.exitCode, 0);
  const catalog = JSON.parse(result.stdout);
  assert.equal(catalog.models.some((model) => model.provider === "cursor"), true);
  assert.equal(result.stdout.includes("synthetic-cursor-key"), false);
});

test("one bounded JSONL request succeeds without leaking environment or credentials", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });
  const result = await runHelper('{"operation":"models"}\n', { HOME: home, authPath });
  assert.equal(result.exitCode, 0);
  assert.deepEqual(JSON.parse(result.stdout), { models: [] });
  assert.equal(result.stderr, "");
  assert.equal(result.stdout.includes("do-not-leak"), false);
});

test("unknown, malformed, oversized, and repeated requests fail with a safe result", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });
  for (const input of ["{}\n", "not-json\n", `${"x".repeat(65 * 1024)}\n`, "{\"operation\":\"models\",\"operation\":\"models\"}\n", "{\"operation\":\"models\"}\n{\"operation\":\"models\"}\n"]) {
    const result = await runHelper(input, { HOME: home, authPath });
    assert.equal(result.exitCode, 1);
    assert.deepEqual(JSON.parse(result.stdout), { error: "model catalog unavailable" });
    assert.equal(result.stdout.includes("do-not-leak"), false);
  }
});

test("invalid protected auth storage fails closed without exposing the parser error", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "not-json", { mode: 0o600 });
  const result = await runHelper('{"operation":"models"}\n', { HOME: home, authPath });
  assert.equal(result.exitCode, 1);
  assert.deepEqual(JSON.parse(result.stdout), { error: "model catalog unavailable" });
  assert.equal(result.stdout.includes("not-json"), false);
  assert.equal(result.stdout.includes(authPath), false);
});
