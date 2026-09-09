import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { discoverModels } from "pi-cursor-sdk/dist/model-discovery.js";
import { streamCursorLazy } from "pi-cursor-sdk/dist/cursor-provider-lazy.js";
import { projectCatalog } from "./protocol.mjs";

const CATALOG_TIMEOUT_MS = 5_000;
const PROVIDERS = {
  "openai-codex": { label: "ChatGPT", authType: "oauth" },
  google: { label: "Google", authType: "api_key" },
  cursor: { label: "Cursor", authType: "api_key" },
};

function modelSummary(model) {
  return {
    provider: model.provider,
    id: model.id,
    name: typeof model.name === "string" && model.name !== "" ? model.name : model.id,
    available: true,
  };
}

async function createRuntime({ authPath, signal }) {
  if (typeof authPath !== "string" || authPath.length === 0) throw new Error("managed auth path is required");
  const runtime = await ModelRuntime.create({
    authPath,
    modelsPath: null,
    refreshOnCreate: false,
    allowModelNetwork: false,
    signal,
  });
  // The exact pinned SDK remains isolated to managed Pi/helper wiring. Its key
  // is resolved from Pi's credential store, never copied into router state.
  runtime.registerProvider("cursor", {
    name: "Cursor",
    baseUrl: "https://cursor.com",
    api: "cursor-sdk",
    apiKey: "$CURSOR_API_KEY",
    models: [],
    streamSimple: streamCursorLazy,
  });
  return runtime;
}

async function refreshCursorCatalog(runtime, signal) {
  let cursorApiKey;
  try {
    const cursorAuth = await runtime.getAuth("cursor", { signal });
    cursorApiKey = typeof cursorAuth?.auth?.apiKey === "string" ? cursorAuth.auth.apiKey : undefined;
  } catch {
    // A signed-out Cursor account has no dynamic models; the rest of the
    // provider snapshot remains useful and no error detail leaves this helper.
    return;
  }
  if (!cursorApiKey) return;
  const cursorModels = await discoverModels({ apiKey: cursorApiKey });
  runtime.registerProvider("cursor", {
    name: "Cursor",
    baseUrl: "https://cursor.com",
    api: "cursor-sdk",
    apiKey: "$CURSOR_API_KEY",
    models: cursorModels,
    streamSimple: streamCursorLazy,
  });
  await runtime.refresh({ allowNetwork: false, providers: ["cursor"], signal });
}

async function snapshot(runtime, signal) {
  await refreshCursorCatalog(runtime, signal);
  const statuses = Object.entries(PROVIDERS).map(([provider, config]) => {
    const auth = runtime.getProviderAuthStatus(provider);
    return {
      provider,
      label: config.label,
      authenticated: auth.configured === true,
      status: auth.configured === true ? "authenticated" : "signed_out",
      ...(typeof auth.label === "string" && auth.label !== "" ? { account_label: auth.label.slice(0, 256) } : {}),
    };
  });
  const authenticated = new Set(statuses.filter((status) => status.authenticated).map((status) => status.provider));
  const available = await runtime.getAvailable(undefined, { signal });
  // Built-in static lists can exist even when a provider has no usable auth.
  // They are not a launch catalog and must never make status look authenticated.
  return { providers: statuses, models: projectCatalog(available.filter((model) => authenticated.has(model.provider)).map(modelSummary)).models };
}

export async function discoverAvailableModels({ authPath }) {
  const signal = AbortSignal.timeout(CATALOG_TIMEOUT_MS);
  const runtime = await createRuntime({ authPath, signal });
  return projectCatalog((await snapshot(runtime, signal)).models);
}

// executeProviderOperation accepts only one mapped provider. All interaction
// callbacks are supplied by index.mjs, which turns them into bounded JSONL
// prompts and waits for a single prompt-response record.
export async function executeProviderOperation({ authPath, provider, operation, interaction, signal }) {
  const config = PROVIDERS[provider];
  if (!config || (operation !== "login" && operation !== "logout")) throw new Error("unsupported provider operation");
  const runtime = await createRuntime({ authPath, signal });
  if (operation === "logout") {
    await runtime.logout(provider, { signal });
  } else {
    await runtime.login(provider, config.authType, interaction);
  }
  return snapshot(runtime, signal);
}
