#!/usr/bin/env node
import process from "node:process";
import { createInterface } from "node:readline";

const input = createInterface({ input: process.stdin });
input.on("line", (line) => {
  try {
    const request = JSON.parse(line);
    if (request.operation === "models") {
      process.stdout.write(`${JSON.stringify({
        providers: [
          { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated" },
          { provider: "google", label: "Google", authenticated: false, status: "signed_out" },
          { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out" },
        ],
        models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "Functional fixture", available: true }],
      })}\n`);
    }
  } catch {
    // The helper boundary deliberately emits no diagnostics for malformed input.
  }
});
