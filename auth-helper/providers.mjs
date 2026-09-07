import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { discoverModels } from "pi-cursor-sdk/dist/model-discovery.js";
import { streamCursorLazy } from "pi-cursor-sdk/dist/cursor-provider-lazy.js";
import { projectCatalog } from "./protocol.mjs";

const CATALOG_TIMEOUT_MS = 5_000;

function modelSummary(model) {
  return {
    provider: model.provider,
    id: model.id,
    name: typeof model.name === "string" && model.name !== "" ? model.name : model.id,
    available: true,
  };
}

export async function discoverAvailableModels({ authPath }) {
  if (typeof authPath !== "string" || authPath.length === 0) throw new Error("managed auth path is required");
  const signal = AbortSignal.timeout(CATALOG_TIMEOUT_MS);
  const runtime = await ModelRuntime.create({
    authPath,
    modelsPath: null,
    refreshOnCreate: false,
    allowModelNetwork: false,
    signal,
  });

  // pi-cursor-sdk is a checked, direct dependency. Register it through
  // ModelRuntime first so its protected auth.json credential can be resolved;
  // only the resulting API key is passed into discovery, never to stdout or
  // the child process environment.
  const cursorConfig = {
    name: "Cursor",
    baseUrl: "https://cursor.com",
    api: "cursor-sdk",
    apiKey: "$CURSOR_API_KEY",
    models: [],
    streamSimple: streamCursorLazy,
  };
  runtime.registerProvider("cursor", cursorConfig);
  let cursorApiKey;
  try {
    const cursorAuth = await runtime.getAuth("cursor", { signal });
    cursorApiKey = typeof cursorAuth?.auth?.apiKey === "string" ? cursorAuth.auth.apiKey : undefined;
  } catch {
    // Missing or expired Cursor auth is represented by the fallback catalog;
    // the helper must still expose other authenticated providers.
  }
  const cursorModels = await discoverModels({ apiKey: cursorApiKey });
  runtime.registerProvider("cursor", { ...cursorConfig, models: cursorModels });
  await runtime.refresh({ allowNetwork: false, providers: ["cursor"], signal });
  const available = await runtime.getAvailable(undefined, { signal });
  return projectCatalog(available.map(modelSummary));
}
