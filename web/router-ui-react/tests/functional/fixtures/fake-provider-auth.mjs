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
          { provider: "google", label: "Google", authenticated: true, status: "authenticated" },
          { provider: "google-vertex", label: "Google Vertex AI", authenticated: true, status: "authenticated" },
          { provider: "antigravity", label: "Google Antigravity", authenticated: true, status: "authenticated" },
          { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out" },
        ],
        models: [
          { provider: "openai-codex", id: "gpt-5.6-terra", name: "Functional fixture", available: true },
          { provider: "google", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash", available: true },
          { provider: "google-vertex", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash (Vertex)", available: true },
          { provider: "antigravity", id: "gemini-3.8-flash", name: "Gemini 3.8 Flash (Antigravity)", available: true },
        ],
      })}\n`);
    }
  } catch {
    // The helper boundary deliberately emits no diagnostics for malformed input.
  }
});
