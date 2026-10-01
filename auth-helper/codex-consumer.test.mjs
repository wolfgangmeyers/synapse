import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { consumeCodexCredential } from "./codex-consumer.mjs";
import { useRouterOwnedCodex } from "./providers.mjs";

test("shared-store helper masks Codex refresh and preserves the broad ordinary catalog", async () => {
  const dir = await mkdtemp(join(tmpdir(), "codex-helper-owner-"));
  try {
    const authPath = join(dir, "auth.json");
    const stored = { "openai-codex": { type: "oauth", access: "fake-old-access", refresh: "fake-old-refresh", expires: Date.now() - 60000 }, google: { type: "api_key", key: "fake-google" } };
    await writeFile(authPath, JSON.stringify(stored), { mode: 0o600 });
    const signal = AbortSignal.timeout(5000);
    const runtime = await ModelRuntime.create({ authPath, modelsPath: null, refreshOnCreate: false, allowModelNetwork: false, signal });
    const provider = runtime.getProvider("openai-codex");
    let refreshes = 0;
    runtime.registerNativeProvider({ ...provider, auth: { ...provider.auth, oauth: { ...provider.auth.oauth, refresh: async () => { refreshes++; throw new Error("offline issuer stub"); } } } });
    await assert.rejects(runtime.getAuth("openai-codex", { signal }));
    assert.ok(refreshes > 0, "the unmasked external refresh stub must be reachable");
    const before = refreshes;
    useRouterOwnedCodex(runtime);
    await runtime.refresh({ allowNetwork: false, signal });
    assert.equal(await runtime.getAuth("openai-codex", { signal }), undefined);
    const models = await runtime.getAvailable(undefined, { signal });
    assert.equal(refreshes, before, "helper discovery cannot refresh the owned Codex projection");
    assert.equal(models.some((model) => model.provider === "openai-codex"), false);
    assert.equal(models.some((model) => model.provider === "google"), true);
    assert.deepEqual(JSON.parse(await readFile(authPath, "utf8")), stored, "cached helper auth cannot overwrite credentials");
  } finally { await rm(dir, { recursive: true, force: true }); }
});

// Real ModelRuntime/AuthStorage, no provider request or external service.
test("private Codex consumer preserves ordinary RPC credential shape and other providers", async () => {
  const dir = await mkdtemp(join(tmpdir(), "codex-consumer-"));
  try {
    const authPath = join(dir, "auth.json");
    await writeFile(authPath, JSON.stringify({ google: { type: "api_key", key: "fake-google" } }), { mode: 0o600 });
    const signal = AbortSignal.timeout(5000);
    const runtime = await ModelRuntime.create({ authPath, modelsPath: null, refreshOnCreate: false, allowModelNetwork: false, signal });
    const credential = { type: "oauth", access: "fake-access", refresh: "fake-refresh", expires: Date.now() + 3600000, accountId: "fake-account" };
    await consumeCodexCredential(runtime, credential, signal);
    let stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.deepEqual(stored["openai-codex"], credential);
    assert.deepEqual(stored.google, { type: "api_key", key: "fake-google" });
    const rotated = { ...credential, access: "fake-rotated-access", refresh: "fake-rotated-refresh" };
    await consumeCodexCredential(runtime, rotated, signal);
    stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.deepEqual(stored["openai-codex"], rotated);
    await assert.rejects(consumeCodexCredential(runtime, { ...credential, accountId: "bad\naccount" }, signal));
    assert.deepEqual(JSON.parse(await readFile(authPath, "utf8"))["openai-codex"], rotated);
    const expired = { ...rotated, expires: Date.now() - 60000 };
    await consumeCodexCredential(runtime, expired, signal);
    assert.deepEqual(JSON.parse(await readFile(authPath, "utf8"))["openai-codex"], expired, "import persists an expired record without an independent issuer refresh");
    await consumeCodexCredential(runtime, null, signal);
    stored = JSON.parse(await readFile(authPath, "utf8"));
    assert.equal(stored["openai-codex"], undefined);
    assert.deepEqual(stored.google, { type: "api_key", key: "fake-google" });
  } finally { await rm(dir, { recursive: true, force: true }); }
});
