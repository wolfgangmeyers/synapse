import * as localUndici from "undici";
import http from "node:http";
import https from "node:https";
import net from "node:net";
import tls from "node:tls";
import dns from "node:dns";
import fs from "node:fs";
import { createRequire, syncBuiltinESMExports } from "node:module";
import path from "node:path";

export function isLoopbackHost(hostname) {
  if (typeof hostname !== "string") return false;
  let clean = hostname.trim().toLowerCase();
  // Strip brackets from IPv6
  if (clean.startsWith("[") && clean.endsWith("]")) {
    clean = clean.slice(1, -1);
  }
  // Strip port if formatted as host:port (handling IPv4 and localhost)
  if (!clean.includes(":") && clean.includes(":")) {
    clean = clean.split(":")[0];
  } else if (/^(127\.0\.0\.1|localhost):\d+$/.test(clean)) {
    clean = clean.split(":")[0];
  }
  return clean === "127.0.0.1" || clean === "localhost" || clean === "::1";
}

export function isAllowedLoopbackUrl(urlStr) {
  try {
    const parsed = typeof urlStr === "string" ? new URL(urlStr) : urlStr;
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return false;
    if (!isLoopbackHost(parsed.hostname)) return false;
    if (parsed.port) {
      const portNum = Number(parsed.port);
      if (!Number.isInteger(portNum) || portNum < 1 || portNum > 65535) return false;
    }
    return true;
  } catch {
    return false;
  }
}

// Strict check on MECHA_TEST_ALLOW_HOSTS: never permit non-loopback external hosts
if (process.env.MECHA_TEST_ALLOW_HOSTS) {
  for (const entry of process.env.MECHA_TEST_ALLOW_HOSTS.split(",")) {
    const trimmed = entry.trim();
    if (trimmed) {
      let hostToCheck = trimmed;
      try {
        const u = new URL(trimmed.includes("://") ? trimmed : `http://${trimmed}`);
        hostToCheck = u.hostname;
      } catch {
        hostToCheck = trimmed.replace(/:\d+$/, "");
      }
      if (!isLoopbackHost(hostToCheck)) {
        throw new Error(
          `MECHA_TEST_ALLOW_HOSTS contains forbidden non-loopback host: ${trimmed}`,
        );
      }
    }
  }
}

// 1. Guard DNS lookup
const origDnsLookup = dns.lookup;
dns.lookup = function (hostname, options, callback) {
  let cb = typeof options === "function" ? options : callback;
  if (!isLoopbackHost(hostname)) {
    const err = new Error(`EgressBlocked: DNS lookup forbidden in test: ${hostname}`);
    if (typeof cb === "function") {
      process.nextTick(() => cb(err));
      return;
    }
    throw err;
  }
  return origDnsLookup.apply(this, arguments);
};

// 2. Guard raw net.connect / net.createConnection / Socket.prototype.connect
const origSocketProtoConnect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function (...args) {
  let host = "localhost";
  if (typeof args[0] === "object" && args[0] !== null) {
    host = args[0].host || args[0].hostname || host;
  } else if (typeof args[1] === "string") {
    host = args[1];
  }
  if (typeof host === "string" && host.includes(":")) {
    try { host = new URL(`http://${host}`).hostname; } catch { host = host.split(":")[0]; }
  }
  if (!isLoopbackHost(host)) {
    const err = new Error(`EgressBlocked: raw network socket forbidden to ${host}`);
    process.nextTick(() => this.destroy(err));
    return this;
  }
  return origSocketProtoConnect.apply(this, args);
};

const origNetConnect = net.connect;
net.connect = function (...args) {
  let host = "localhost";
  if (typeof args[0] === "object" && args[0] !== null) {
    host = args[0].host || args[0].hostname || host;
  } else if (typeof args[1] === "string") {
    host = args[1];
  }
  if (typeof host === "string" && host.includes(":")) {
    try { host = new URL(`http://${host}`).hostname; } catch { host = host.split(":")[0]; }
  }
  if (!isLoopbackHost(host)) {
    const err = new Error(`EgressBlocked: raw network socket forbidden to ${host}`);
    const socket = new net.Socket();
    process.nextTick(() => socket.destroy(err));
    return socket;
  }
  return origNetConnect.apply(this, args);
};
net.createConnection = net.connect;

// 3. Guard TLS connect
const origTlsConnect = tls.connect;
tls.connect = function (...args) {
  let host = "localhost";
  if (typeof args[0] === "object" && args[0] !== null) {
    host = args[0].host || args[0].servername || args[0].hostname || host;
  } else if (typeof args[1] === "string") {
    host = args[1];
  }
  if (typeof host === "string" && host.includes(":")) {
    try { host = new URL(`https://${host}`).hostname; } catch { host = host.split(":")[0]; }
  }
  if (!isLoopbackHost(host)) {
    const err = new Error(`EgressBlocked: TLS connect forbidden to ${host}`);
    const socket = new net.Socket();
    process.nextTick(() => socket.destroy(err));
    return socket;
  }
  return origTlsConnect.apply(this, args);
};

// 3. Guard standard node:http and node:https against non-loopback egress
function patchNodeHttp() {
  const patchModule = (mod) => {
    const origRequest = mod.request;
    mod.request = function (input, options, cb) {
      let urlObj;
      if (typeof input === "string" || input instanceof URL) {
        try { urlObj = new URL(input); } catch {}
      } else if (input && typeof input === "object") {
        urlObj = input;
      }
      let hostname = urlObj?.hostname || urlObj?.host || "";
      let path = urlObj?.pathname || urlObj?.path || "/";
      let method = (urlObj?.method || "GET").toUpperCase();
      if (!hostname && options && typeof options === "object") {
        hostname = options.hostname || options.host || "";
        path = options.path || "/";
        if (options.method) method = String(options.method).toUpperCase();
      }

      if (hostname.includes(":")) {
        try {
          hostname = new URL(`http://${hostname}`).hostname;
        } catch {
          hostname = hostname.split(":")[0];
        }
      }

      // Allow mock token resolution via standard node:https for GoogleAuth / Vertex with exact POST method
      if (
        mod === https &&
        hostname === "oauth2.googleapis.com" &&
        (path === "/token" || path.startsWith("/token?")) &&
        method === "POST"
      ) {
        let callback = cb;
        if (typeof options === "function") callback = options;
        const req = new http.ClientRequest({ createConnection: () => {} });
        const origEnd = req.end;
        req.end = function (...endArgs) {
          origEnd.apply(this, endArgs);
          process.nextTick(() => {
            const tokenResp = JSON.stringify({
              access_token: "synthetic-vertex-access-token",
              expires_in: 3600,
              token_type: "Bearer",
            });
            const res = new http.IncomingMessage(new net.Socket());
            res.statusCode = 200;
            res.headers = {
              "content-type": "application/json; charset=utf-8",
              "content-length": String(Buffer.byteLength(tokenResp)),
            };
            if (typeof callback === "function") callback(res);
            req.emit("response", res);
            res.push(Buffer.from(tokenResp));
            res.push(null);
          });
          return req;
        };
        return req;
      }

      if (!isLoopbackHost(hostname)) {
        const err = new Error(
          `EgressBlocked: external network call forbidden in test: ${hostname}`,
        );
        const req = new http.ClientRequest({ createConnection: () => {} });
        process.nextTick(() => req.emit("error", err));
        return req;
      }
      return origRequest.apply(this, arguments);
    };
    mod.get = function () {
      const req = mod.request.apply(this, arguments);
      req.end();
      return req;
    };
  };
  patchModule(http);
  patchModule(https);
  syncBuiltinESMExports();
}
patchNodeHttp();

// Resolve host undici safely from configured Antigravity extension path if present
let hostUndici = null;
const agEntry = process.env.MECHA_ANTIGRAVITY_EXTENSION?.trim();
if (agEntry) {
  try {
    const agRequire = createRequire(agEntry);
    const hostUndiciPath = agRequire.resolve("undici");
    hostUndici = await import(hostUndiciPath);
  } catch {
    // host undici not present or unresolvable
  }
}

// Resolve CLI undici from process.argv[1] (pi CLI entry realpath) or fallback to global package
let cliUndici = null;
if (process.argv[1]) {
  try {
    const realCli = fs.realpathSync(process.argv[1]);
    const cliReq = createRequire(realCli);
    const cliUndiciPath = cliReq.resolve("undici");
    cliUndici = await import(cliUndiciPath);
  } catch {
    // CLI argv resolve failed
  }
}
if (!cliUndici) {
  try {
    const globalNodeModules = path.resolve(process.execPath, "../../lib/node_modules");
    const piUndiciPath = path.join(globalNodeModules, "@earendil-works/pi-coding-agent/node_modules/undici/index.js");
    cliUndici = await import(piUndiciPath);
  } catch {
    // pi undici not present
  }
}
const piUndici = cliUndici;

// Pure synchronous body decoder to prevent UND_ERR_INVALID_ARG in Undici 5
function syncExtractBodyString(body) {
  if (!body) return "";
  if (typeof body === "string") return body;
  if (Buffer.isBuffer(body)) return body.toString("utf8");
  if (ArrayBuffer.isView(body)) {
    return Buffer.from(body.buffer, body.byteOffset, body.byteLength).toString("utf8");
  }
  if (body instanceof ArrayBuffer) {
    return Buffer.from(body).toString("utf8");
  }
  if (body instanceof URLSearchParams) {
    return body.toString();
  }
  if (typeof body === "object") {
    try {
      return JSON.stringify(body);
    } catch {}
  }
  return String(body || "");
}

let mockDispatchDepth = 0;

export function isKnownMockOrigin(origin) {
  if (typeof origin !== "string") return false;
  if (
    origin === "https://oauth2.googleapis.com" ||
    origin === "https://www.googleapis.com" ||
    origin === "https://aiplatform.googleapis.com" ||
    origin.endsWith("-aiplatform.googleapis.com")
  ) {
    return true;
  }
  return false;
}

function configureMockAgent(undiciPkg) {
  if (!undiciPkg || typeof undiciPkg.MockAgent !== "function") return null;
  const agent = new undiciPkg.MockAgent();
  agent.disableNetConnect();

  const origDispatch = agent.dispatch.bind(agent);
  agent.dispatch = function (opts, handler) {
    let pathname = "";
    let origin = "";
    try {
      const o = typeof opts.origin === "string" ? opts.origin : (opts.origin ? opts.origin.origin : "");
      origin = o || "";
      const p = typeof opts.path === "string" ? opts.path : (opts.pathname || "/");
      pathname = p.split("?")[0];
    } catch {}
    const method = String(opts.method || "GET").toUpperCase();

    if (handler && typeof handler.onError === "function") {
      const origOnError = handler.onError;
      handler.onError = function (err) {
        if (err) {
          const code = err.code || err.cause?.code || err.cause?.name || err.name || "ERR_TRANSPORT";
          const diag = `[DIAG: code=${code} method=${method} origin=${origin} path=${pathname}]`;
          if (!err.message || !err.message.includes("[DIAG:")) {
            err.message = `${err.message || ""} ${diag}`.trim();
          }
        }
        return origOnError.apply(this, arguments);
      };
    }

    mockDispatchDepth++;
    try {
      return origDispatch(opts, handler);
    } catch (err) {
      if (err) {
        const code = err.code || err.cause?.code || err.cause?.name || err.name || "ERR_TRANSPORT";
        const diag = `[DIAG: code=${code} method=${method} origin=${origin} path=${pathname}]`;
        if (!err.message || !err.message.includes("[DIAG:")) {
          err.message = `${err.message || ""} ${diag}`.trim();
        }
      }
      throw err;
    } finally {
      mockDispatchDepth--;
    }
  };

  // Strict anchored loopback predicate: never match localhost.evil or 127.0.0.1.evil
  agent.enableNetConnect((host) => {
    let hostname = host;
    try {
      const u = new URL(host.includes("://") ? host : `http://${host}`);
      hostname = u.hostname;
    } catch {
      hostname = host.replace(/:\d+$/, "");
    }
    return isLoopbackHost(hostname);
  });

  // 1. OAuth token endpoint: token exchange and token refresh (SYNC reply)
  const oauthClient = agent.get("https://oauth2.googleapis.com");
  oauthClient
    .intercept({ path: "/token", method: "POST" })
    .reply((opts) => {
      const rawBody = syncExtractBodyString(opts.body) || syncExtractBodyString(opts.__mockAgentBodyForDispatch);
      if (!rawBody || typeof rawBody !== "string") {
        return {
          statusCode: 400,
          data: { error: "invalid_request", error_description: "Missing or empty request body" },
        };
      }
      const trimmed = rawBody.trim();
      if (!trimmed) {
        return {
          statusCode: 400,
          data: { error: "invalid_request", error_description: "Empty request body" },
        };
      }

      let parsed = {};
      if (trimmed.startsWith("{") && trimmed.endsWith("}")) {
        try {
          parsed = JSON.parse(trimmed);
        } catch {
          return {
            statusCode: 400,
            data: { error: "invalid_request", error_description: "Malformed JSON request body" },
          };
        }
      } else {
        try {
          const params = new URLSearchParams(trimmed);
          for (const [k, v] of params.entries()) {
            parsed[k] = v;
          }
        } catch {
          return {
            statusCode: 400,
            data: { error: "invalid_request", error_description: "Malformed URL-encoded parameters" },
          };
        }
      }

      const grantType = parsed.grant_type;
      if (!grantType) {
        return {
          statusCode: 400,
          data: { error: "invalid_request", error_description: "Missing grant_type parameter" },
        };
      }

      if (grantType === "authorization_code") {
        if (!parsed.code) {
          return {
            statusCode: 400,
            data: { error: "invalid_request", error_description: "Missing code parameter" },
          };
        }
        if (
          process.env.MECHA_TEST_MOCK_FAIL_TOKEN_EXCHANGE === "true" ||
          parsed.code === "invalid-code" ||
          trimmed.includes("code=invalid-code")
        ) {
          return {
            statusCode: 400,
            data: {
              error: "invalid_grant",
              error_description: "Invalid authorization code",
            },
          };
        }

        const accessToken =
          process.env.MECHA_TEST_MOCK_ACCESS_TOKEN || "synthetic-antigravity-access-token";
        const refreshToken =
          process.env.MECHA_TEST_MOCK_REFRESH_TOKEN || "synthetic-antigravity-refresh-token";
        return {
          statusCode: 200,
          data: {
            access_token: accessToken,
            refresh_token: refreshToken,
            expires_in: 3600,
            token_type: "Bearer",
          },
        };
      }

      if (grantType === "refresh_token") {
        if (!parsed.refresh_token) {
          return {
            statusCode: 400,
            data: { error: "invalid_request", error_description: "Missing refresh_token parameter" },
          };
        }
        if (
          process.env.MECHA_TEST_MOCK_FAIL_REFRESH === "true" ||
          parsed.refresh_token === "expired-refresh-token" ||
          trimmed.includes("refresh_token=expired-refresh-token")
        ) {
          return {
            statusCode: 400,
            data: {
              error: "invalid_grant",
              error_description: "Token has been expired or revoked",
            },
          };
        }

        const accessToken =
          process.env.MECHA_TEST_MOCK_ACCESS_TOKEN || "synthetic-refreshed-antigravity-access-token";
        const refreshToken =
          process.env.MECHA_TEST_MOCK_REFRESH_TOKEN || "synthetic-antigravity-refresh-token";
        return {
          statusCode: 200,
          data: {
            access_token: accessToken,
            refresh_token: refreshToken,
            expires_in: 3600,
            token_type: "Bearer",
          },
        };
      }

      return {
        statusCode: 400,
        data: {
          error: "unsupported_grant_type",
          error_description: `Unsupported grant_type: ${grantType}`,
        },
      };
    })
    .persist();

  // 2. Userinfo endpoint: exact bounded endpoint (SYNC reply)
  const googleClient = agent.get("https://www.googleapis.com");
  googleClient
    .intercept({
      path: (p) => p === "/oauth2/v1/userinfo?alt=json" || p === "/oauth2/v1/userinfo",
      method: "GET",
    })
    .reply((_opts) => {
      if (process.env.MECHA_TEST_MOCK_FAIL_USERINFO === "true") {
        return {
          statusCode: 401,
          data: { error: { message: "Invalid Credentials" } },
        };
      }

      const email = process.env.MECHA_TEST_MOCK_USER_EMAIL || "synthetic-user@example.com";
      return {
        statusCode: 200,
        data: { email },
      };
    })
    .persist();

  undiciPkg.setGlobalDispatcher(agent);
  const kGlobal1 = Symbol.for("undici.globalDispatcher.1");
  const kGlobal2 = Symbol.for("undici.globalDispatcher.2");
  for (const k of [kGlobal1, kGlobal2]) {
    const wrapper = globalThis[k];
    if (wrapper) {
      ensureMockActive(wrapper);
      ensureMockActive(Object.getPrototypeOf(wrapper));
    }
  }
  return agent;
}

function ensureMockActive(proto) {
  if (!proto) return;
  if (Object.prototype.hasOwnProperty.call(proto, "isMockActive")) return;
  try {
    Object.defineProperty(proto, "isMockActive", {
      get() { return true; },
      configurable: true,
      enumerable: false,
    });
  } catch {}
}

function hookUndiciPrototypes(undiciPkg, mockAgent) {
  if (!undiciPkg || !mockAgent) return;

  const targets = [];
  if (undiciPkg.Agent?.prototype) {
    const DispatcherBase = Object.getPrototypeOf(undiciPkg.Agent.prototype);
    if (DispatcherBase && typeof DispatcherBase.dispatch === "function" && !targets.includes(DispatcherBase)) {
      targets.push(DispatcherBase);
    }
    if (typeof undiciPkg.Agent.prototype.dispatch === "function" && !targets.includes(undiciPkg.Agent.prototype)) {
      targets.push(undiciPkg.Agent.prototype);
    }
  }
  if (undiciPkg.EnvHttpProxyAgent?.prototype) {
    if (typeof undiciPkg.EnvHttpProxyAgent.prototype.dispatch === "function" && !targets.includes(undiciPkg.EnvHttpProxyAgent.prototype)) {
      targets.push(undiciPkg.EnvHttpProxyAgent.prototype);
    }
  }

  for (const proto of targets) {
    ensureMockActive(proto);
    if (proto.__mecha_agent_hooked__) continue;
    proto.__mecha_agent_hooked__ = true;
    const origProtoDispatch = proto.dispatch;
    proto.dispatch = function (opts, handler) {
      if (mockDispatchDepth > 0) {
        return origProtoDispatch.call(this, opts, handler);
      }
      let origin = "";
      try {
        const o = typeof opts.origin === "string" ? opts.origin : (opts.origin ? opts.origin.origin : "");
        origin = o || "";
      } catch {}

      if (isKnownMockOrigin(origin)) {
        return mockAgent.dispatch(opts, handler);
      }
      return origProtoDispatch.call(this, opts, handler);
    };
  }
}

export const localMockAgent = configureMockAgent(localUndici);
export const hostMockAgent = hostUndici && hostUndici !== localUndici ? configureMockAgent(hostUndici) : null;
export const piMockAgent = piUndici && piUndici !== localUndici && piUndici !== hostUndici ? configureMockAgent(piUndici) : null;

// Hook global dispatcher symbols to catch any late setGlobalDispatcher calls (including Pi bundled Undici)
const origDefineProperty = Object.defineProperty;
const kGlobal1 = Symbol.for("undici.globalDispatcher.1");
const kGlobal2 = Symbol.for("undici.globalDispatcher.2");

Object.defineProperty = function (obj, prop, desc) {
  if (
    obj === globalThis &&
    (prop === kGlobal1 || prop === kGlobal2) &&
    desc &&
    desc.value &&
    typeof desc.value.dispatch === "function"
  ) {
    ensureMockActive(desc.value);
    ensureMockActive(Object.getPrototypeOf(desc.value));
    const origDispatcherDispatch = desc.value.dispatch;
    desc.value.dispatch = function (opts, handler) {
      if (mockDispatchDepth > 0) {
        return origDispatcherDispatch.call(this, opts, handler);
      }
      let origin = "";
      try {
        const o = typeof opts.origin === "string" ? opts.origin : (opts.origin ? opts.origin.origin : "");
        origin = o || "";
      } catch {}

      if (isKnownMockOrigin(origin)) {
        const activeAgent = hostMockAgent || piMockAgent || localMockAgent;
        if (activeAgent) {
          mockDispatchDepth++;
          try {
            return activeAgent.dispatch(opts, handler);
          } finally {
            mockDispatchDepth--;
          }
        }
      }
      return origDispatcherDispatch.call(this, opts, handler);
    };
  }
  return origDefineProperty.call(this, obj, prop, desc);
};

hookUndiciPrototypes(localUndici, localMockAgent);
if (hostUndici) hookUndiciPrototypes(hostUndici, hostMockAgent || localMockAgent);
if (piUndici) hookUndiciPrototypes(piUndici, piMockAgent || hostMockAgent || localMockAgent);

let normalizedFetch = async function (input, init = {}) {
  let urlStr = "";
  if (typeof input === "string") urlStr = input;
  else if (input instanceof URL) urlStr = input.href;
  else if (input && typeof input === "object" && "url" in input) urlStr = String(input.url);

  let options = { ...init };
  if (input && typeof input === "object" && typeof input.headers?.forEach === "function") {
    const reqHeaders = {};
    input.headers.forEach((v, k) => { reqHeaders[k] = v; });
    options.headers = { ...reqHeaders, ...(init.headers || {}) };
    if (!options.method && input.method) options.method = input.method;
  }

  let pathname = "";
  let origin = "";
  try {
    const parsed = new URL(urlStr || (input && input.url) || "http://localhost");
    origin = parsed.origin;
    pathname = parsed.pathname;
  } catch {
    pathname = String(urlStr).split("?")[0];
  }
  const method = String(options.method || (input && input.method) || "GET").toUpperCase();

  try {
    return await localUndici.fetch(urlStr || input, {
      ...options,
      dispatcher: localMockAgent,
    });
  } catch (err) {
    if (err) {
      const code = err.code || err.cause?.code || err.cause?.name || err.name || "ERR_FETCH";
      const diag = `[DIAG: code=${code} method=${method} origin=${origin} path=${pathname}]`;
      if (!err.message || !err.message.includes("[DIAG:")) {
        err.message = `${err.message || ""} ${diag}`.trim();
      }
    }
    throw err;
  }
};

globalThis.fetch = normalizedFetch;
Object.defineProperty(globalThis, "fetch", {
  configurable: true,
  enumerable: true,
  get() {
    return normalizedFetch;
  },
  set(newFn) {
    // Keep normalizedFetch active across late overrides by Pi runtime
  },
});

globalThis.__MECHA_MOCK_AGENT__ = localMockAgent;
if (hostMockAgent) {
  globalThis.__MECHA_HOST_MOCK_AGENT__ = hostMockAgent;
}
if (piMockAgent) {
  globalThis.__MECHA_PI_MOCK_AGENT__ = piMockAgent;
}
globalThis.__MECHA_MOCK_AGENTS__ = [localMockAgent, hostMockAgent, piMockAgent].filter(Boolean);
