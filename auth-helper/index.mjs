#!/usr/bin/env node

import { createInterface } from "node:readline";
import { discoverAvailableModels, executeProviderOperation } from "./providers.mjs";
import { encodeResult, parseRequest, parseResponse, promptEvent, safeError, terminalResult } from "./protocol.mjs";

const OPERATION_TIMEOUT_MS = 10 * 60 * 1000;

class LineQueue {
  #values = [];
  #waiters = [];
  #closed = false;
  #closedPromise = new Promise((resolve) => { this.resolveClosed = resolve; });

  push(value) {
    const waiter = this.#waiters.shift();
    if (waiter) waiter.resolve(value);
    else this.#values.push(value);
  }

  close() {
    this.#closed = true;
    this.resolveClosed();
    for (const waiter of this.#waiters.splice(0)) waiter.reject(new Error("input closed"));
  }

  async closed() { await this.#closedPromise; }
  get length() { return this.#values.length; }

  next(signal) {
    if (this.#values.length > 0) return Promise.resolve(this.#values.shift());
    if (this.#closed) return Promise.reject(new Error("input closed"));
    return new Promise((resolve, reject) => {
      const waiter = { resolve, reject };
      this.#waiters.push(waiter);
      const abort = () => {
        const index = this.#waiters.indexOf(waiter);
        if (index >= 0) this.#waiters.splice(index, 1);
        reject(new Error("aborted"));
      };
      if (signal) {
        if (signal.aborted) abort();
        else signal.addEventListener("abort", abort, { once: true });
      }
    });
  }
}

function emit(value) { process.stdout.write(encodeResult(value)); }

const input = createInterface({ input: process.stdin, crlfDelay: Infinity });
const lines = new LineQueue();
let inputBytes = 0;
let oversized = false;
input.on("line", (line) => {
  inputBytes += Buffer.byteLength(line, "utf8") + 1;
  if (inputBytes > 128 * 1024) oversized = true;
  lines.push(line);
});
input.on("close", () => lines.close());

let request;
try {
  const first = await lines.next();
  if (oversized) throw new Error("input too large");
  request = parseRequest(first);
  if (request.operation === "models") {
    // The retained catalog-only shape is strictly one request as well. Waiting
    // for EOF prevents a second JSONL line from being ignored as it was in P3.
    await lines.closed();
    if (oversized || lines.length !== 0) throw new Error("invalid request count");
    emit(await discoverAvailableModels({ authPath: process.env.MECHA_MANAGED_AUTH_PATH }));
  } else {
    const controller = new AbortController();
    const expiration = setTimeout(() => controller.abort(), OPERATION_TIMEOUT_MS);
    let nextPrompt = 0;
    const interaction = {
      signal: controller.signal,
      notify() {
        // Provider notices can contain external SDK strings. The router only
        // needs the bounded pending state already emitted below.
      },
      async prompt(prompt) {
        const id = `prompt-${++nextPrompt}`;
        emit(promptEvent(request, {
          id,
          kind: prompt.type,
          message: prompt.message,
          ...(typeof prompt.placeholder === "string" ? { placeholder: prompt.placeholder } : {}),
        }));
        while (true) {
          const response = parseResponse(await lines.next(controller.signal));
          if (response.prompt_id === id) return response.value;
          // A stale or cross-operation value is a protocol violation, not a
          // retry channel that might apply a credential to the wrong prompt.
          throw new Error("unexpected prompt response");
        }
      },
    };
    emit({ type: "state", operation_id: request.operation_id, provider: request.provider, status: "pending" });
    try {
      const result = await executeProviderOperation({
        authPath: process.env.MECHA_MANAGED_AUTH_PATH,
        provider: request.provider,
        operation: request.operation,
        interaction,
        signal: controller.signal,
      });
      emit(terminalResult(request, "succeeded", result));
    } catch {
      emit(safeError(request));
      process.exitCode = 1;
    } finally {
      clearTimeout(expiration);
    }
  }
} catch {
  try { emit(safeError(request)); } catch { /* no unsafe fallback output */ }
  process.exitCode = 1;
} finally {
  input.close();
  process.stdin.destroy();
}
