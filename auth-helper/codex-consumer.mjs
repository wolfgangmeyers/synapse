#!/usr/bin/env node

// Private stdin-only boundary for ordinary RPC Pi. Embedded requests never
// read this projection: their authoritative credential is the Go-owned file.
import { ModelRuntime, CredentialSynchronizationError } from "@earendil-works/pi-coding-agent";
import { createInterface } from "node:readline";
import { pathToFileURL } from "node:url";

export async function consumeCodexCredential(runtime, value, signal) {
  if (value === null) { await runtime.logout("openai-codex", { signal }); return; }
  if (typeof value !== "object" || Array.isArray(value) || Object.keys(value).length !== 5 || value.type !== "oauth" ||
      typeof value.access !== "string" || value.access.length < 1 || value.access.length > 32768 ||
      typeof value.refresh !== "string" || value.refresh.length < 1 || value.refresh.length > 32768 ||
      typeof value.accountId !== "string" || !/^[A-Za-z0-9_-]{1,256}$/.test(value.accountId) ||
      !Number.isSafeInteger(value.expires) || value.expires <= 0) throw new Error("invalid credential");
  const provider = runtime.getProvider("openai-codex");
  if (!provider?.auth?.oauth) throw new Error("Codex provider is unavailable");
  // Reuse ModelRuntime's credential-update hook. Acquisition already happened
  // in Go; this fixed private provider adapter only returns that credential.
  runtime.registerNativeProvider({ ...provider, auth: { ...provider.auth, oauth: {
    ...provider.auth.oauth, login: async () => value,
    refresh: async () => { throw new Error("refresh belongs to the Go credential owner"); },
  } } });
  try {
    await runtime.login("openai-codex", "oauth", { signal, prompt: async () => { throw new Error("unexpected prompt"); }, notify() {} });
  } catch (error) {
    // ModelRuntime can commit an expired credential and then fail its local
    // availability refresh. Accept only that documented post-commit error
    // carrying this exact record. Storage failures and cancellation still fail.
    // Go owns the subsequent refresh and public availability projection.
    const saved = error instanceof CredentialSynchronizationError ? error.credential : undefined;
    if (signal?.aborted || !(error instanceof CredentialSynchronizationError) || error.providerId !== "openai-codex" || error.operation !== "login" ||
        saved?.type !== value.type || saved.access !== value.access || saved.refresh !== value.refresh || saved.expires !== value.expires || saved.accountId !== value.accountId) throw new Error("credential synchronization failed");
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
  let count = 0;
  let bytes = 0;
  let value;
  try {
    for await (const line of input) {
      bytes += Buffer.byteLength(line, "utf8");
      if (++count !== 1 || bytes > 65536) throw new Error("invalid input");
      value = JSON.parse(line);
    }
    if (count !== 1 || typeof process.env.MECHA_PI_AUTH_PATH !== "string" || process.env.MECHA_PI_AUTH_PATH === "") throw new Error("invalid input");
    const signal = AbortSignal.timeout(5000);
    const runtime = await ModelRuntime.create({ authPath: process.env.MECHA_PI_AUTH_PATH, modelsPath: null, refreshOnCreate: false, allowModelNetwork: false, signal });
    await consumeCodexCredential(runtime, value, signal);
    process.stdout.write('{"synchronized":true}\n');
  } catch {
    // No exceptions, credential values, or raw SDK output cross this boundary.
    process.exitCode = 1;
  } finally { input.close(); }
}
