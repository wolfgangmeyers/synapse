import path from "node:path";
import os from "node:os";
import fs from "node:fs/promises";
import { ModelRuntime } from "@earendil-works/pi-coding-agent";
import { discoverModels } from "pi-cursor-sdk/dist/model-discovery.js";
import { streamCursorLazy } from "pi-cursor-sdk/dist/cursor-provider-lazy.js";
import { projectCatalog } from "./protocol.mjs";

const CATALOG_TIMEOUT_MS = 5_000;
const PROVIDERS = {
  "openai-codex": { label: "ChatGPT", authType: "oauth" },
  google: { label: "Google", authType: "api_key" },
  cursor: { label: "Cursor", authType: "api_key" },
  "google-vertex": { label: "Google Vertex AI", authType: "api_key" },
};

function modelSummary(model) {
  return {
    provider: model.provider,
    id: model.id,
    name: typeof model.name === "string" && model.name !== "" ? model.name : model.id,
    available: true,
  };
}

export function resolveHostAdcPath() {
  if (process.env.GOOGLE_APPLICATION_CREDENTIALS && process.env.GOOGLE_APPLICATION_CREDENTIALS.trim()) {
    return path.resolve(process.env.GOOGLE_APPLICATION_CREDENTIALS.trim());
  }
  if (process.env.CLOUDSDK_CONFIG && process.env.CLOUDSDK_CONFIG.trim()) {
    return path.resolve(process.env.CLOUDSDK_CONFIG.trim(), "application_default_credentials.json");
  }
  const hostHome = process.env.MECHA_HOST_HOME?.trim() || os.homedir();
  return path.resolve(hostHome, ".config", "gcloud", "application_default_credentials.json");
}

// Shared-store helper discovery must neither resolve nor refresh the ordinary
// Pi Codex projection. Go supplies Codex status/catalog under its file lock.
export function useRouterOwnedCodex(runtime) {
  const provider = runtime.getProvider("openai-codex");
  if (!provider) throw new Error("Codex provider is unavailable");
  runtime.registerNativeProvider({
    ...provider,
    auth: {
      apiKey: {
        name: "Router-owned Codex",
        resolve: async () => undefined,
        check: async () => undefined,
      },
    },
  });
}

// GoogleGenAI interpolates region into the hostname (https://${location}-aiplatform.googleapis.com).
// A safe ASCII single host label (RFC 1123) protects URL authority without requiring a fixed region catalog.
const REGION_LABEL_PATTERN = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i;

function isValidVertexRegion(region) {
  if (typeof region !== "string") return false;
  return REGION_LABEL_PATTERN.test(region);
}

function ensureArgonModel(models, provider, api, baseUrl) {
  if (!Array.isArray(models)) return models;
  if (models.some((m) => m?.id === "gemini-4-argon")) return models;
  const template = models.find((m) => m?.id === "gemini-3.8-flash") || models[0];
  const argon = {
    ...template,
    id: "gemini-4-argon",
    name: "Gemini 4 Argon",
    api: api || template?.api,
    provider: provider || template?.provider,
    baseUrl: baseUrl || template?.baseUrl,
    reasoning: true,
    thinkingLevelMap: template?.thinkingLevelMap || {
      off: null,
      minimal: null,
      low: "low",
      medium: "medium",
      high: "high",
      xhigh: null,
      max: null,
    },
    input: ["text", "image"],
    cost: template?.cost || { input: 0.75, output: 3.75, cacheRead: 0.075, cacheWrite: 0 },
    contextWindow: 1048576,
    maxTokens: 65536,
  };
  return [...models, argon];
}

function registerGoogleProvider(runtime) {
  const google = runtime.getProvider("google");
  if (!google) return;

  const originalGetModels = google.getModels;
  runtime.registerNativeProvider({
    ...google,
    getModels: () => {
      const models = originalGetModels ? originalGetModels() : (google.models || []);
      return ensureArgonModel(models, "google", "google-generative-ai", google.baseUrl);
    },
    auth: {
      apiKey: {
        name: "Gemini API key",
        login: async (interaction) => {
          interaction.signal?.throwIfAborted();
          const rawKey = await interaction.prompt({
            type: "secret",
            message: "Enter Gemini API key",
          });
          interaction.signal?.throwIfAborted();
          const key = typeof rawKey === "string" ? rawKey.trim() : "";
          if (!key) {
            throw new Error("Gemini API key cannot be empty");
          }
          return {
            type: "api_key",
            key,
          };
        },
        resolve: google.auth?.apiKey?.resolve,
      },
    },
  });
}

function registerVertexAdcProvider(runtime) {
  const vertex = runtime.getProvider("google-vertex");
  if (!vertex) return;

  const originalGetModels = vertex.getModels;
  runtime.registerNativeProvider({
    ...vertex,
    getModels: () => {
      const models = originalGetModels ? originalGetModels() : (vertex.models || []);
      return ensureArgonModel(models, "google-vertex", "google-vertex", vertex.baseUrl);
    },
    auth: {
      apiKey: {
        name: "Google Cloud credentials",
        login: async (interaction) => {
          interaction.signal?.throwIfAborted();
          const rawProject = await interaction.prompt({
            type: "text",
            message: "Enter Google Cloud Project ID",
            placeholder: "my-gcp-project",
          });
          interaction.signal?.throwIfAborted();
          const project = typeof rawProject === "string" ? rawProject.trim() : "";
          if (!project) {
            throw new Error("Google Cloud Project ID cannot be empty");
          }
          const locationInput = await interaction.prompt({
            type: "text",
            message: "Enter Google Cloud Region (default: us-central1)",
            placeholder: "us-central1",
          });
          interaction.signal?.throwIfAborted();
          const trimmedLocation = typeof locationInput === "string" ? locationInput.trim() : "";
          const location = trimmedLocation || "us-central1";
          if (!isValidVertexRegion(location)) {
            throw new Error(
              `Invalid Google Cloud region: "${location}". Region must be a valid single host label (alphanumeric and hyphens, 1-63 chars).`,
            );
          }
          const adcPath = resolveHostAdcPath();
          try {
            await fs.access(adcPath);
          } catch {
            throw new Error(
              `Host Application Default Credentials not found at ${adcPath}. Run 'gcloud auth application-default login' on the host first.`,
            );
          }
          return {
            type: "api_key",
            env: {
              GOOGLE_CLOUD_PROJECT: project,
              GOOGLE_CLOUD_LOCATION: location,
              GOOGLE_APPLICATION_CREDENTIALS: adcPath,
            },
          };
        },
        resolve: vertex.auth?.apiKey?.resolve,
      },
    },
  });
}

async function createRuntime({ authPath, signal, ownedCodex = false }) {
  if (typeof authPath !== "string" || authPath.length === 0) {
    throw new Error("ordinary Pi auth path is required");
  }
  const runtime = await ModelRuntime.create({
    authPath,
    modelsPath: null,
    refreshOnCreate: false,
    allowModelNetwork: false,
    signal,
  });

  if (ownedCodex) useRouterOwnedCodex(runtime);
  registerGoogleProvider(runtime);
  registerVertexAdcProvider(runtime);

  // Pinned Cursor SDK registration
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
  // ModelRuntime snapshots credentials during refresh without contacting providers
  await runtime.refresh({ allowNetwork: false, signal });
  await refreshCursorCatalog(runtime, signal);

  const statuses = await Promise.all(
    Object.entries(PROVIDERS).map(async ([provider, config]) => {
      const auth = runtime.getProviderAuthStatus(provider);
      let accountLabel = typeof auth.label === "string" && auth.label !== "" ? auth.label.slice(0, 256) : undefined;
      if (!accountLabel && auth.configured) {
        try {
          const cred = await runtime.credentials.read(provider);
          if (cred?.email && typeof cred.email === "string") {
            accountLabel = cred.email.slice(0, 256);
          } else if (cred?.env?.GOOGLE_CLOUD_PROJECT && typeof cred.env.GOOGLE_CLOUD_PROJECT === "string") {
            accountLabel = cred.env.GOOGLE_CLOUD_PROJECT.slice(0, 256);
          }
        } catch {
          // ignore credential read error
        }
      }
      return {
        provider,
        label: config.label,
        authenticated: auth.configured === true,
        status: auth.configured === true ? "authenticated" : "signed_out",
        ...(accountLabel ? { account_label: accountLabel } : {}),
      };
    }),
  );

  const authenticated = new Set(
    statuses.filter((status) => status.authenticated).map((status) => status.provider),
  );
  const available = await runtime.getAvailable(undefined, { signal });
  return {
    providers: statuses,
    models: projectCatalog(available.filter((model) => authenticated.has(model.provider)).map(modelSummary)).models,
  };
}

export async function discoverProviderSnapshot({ authPath, ownedCodex = false }) {
  const signal = AbortSignal.timeout(CATALOG_TIMEOUT_MS);
  const runtime = await createRuntime({ authPath, signal, ownedCodex });
  return snapshot(runtime, signal);
}

export async function discoverAvailableModels({ authPath }) {
  return projectCatalog((await discoverProviderSnapshot({ authPath })).models);
}

export async function executeProviderOperation({
  authPath,
  provider,
  operation,
  interaction,
  signal,
  ownedCodex = false,
}) {
  const config = PROVIDERS[provider];
  if (!config || (operation !== "login" && operation !== "logout")) {
    throw new Error("unsupported provider operation");
  }
  if (ownedCodex && provider === "openai-codex") {
    throw new Error("Codex auth belongs to the router");
  }

  const runtime = await createRuntime({ authPath, signal, ownedCodex });
  if (operation === "logout") {
    await runtime.logout(provider, { signal });
  } else {
    let authUrl;
    const wrappedInteraction = {
      ...interaction,
      notify(notice) {
        if (notice?.url && typeof notice.url === "string") {
          authUrl = notice.url;
        }
        interaction?.notify?.(notice);
      },
      async prompt(prompt) {
        return interaction.prompt({
          ...prompt,
          ...((prompt.url || authUrl) ? { url: prompt.url || authUrl } : {}),
        });
      },
    };
    await runtime.login(provider, config.authType, wrappedInteraction);
  }
  return snapshot(runtime, signal);
}
