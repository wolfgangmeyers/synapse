import * as localUndici from "undici";
import { getGlobalDispatcher } from "undici";
import http from "node:http";
import https from "node:https";
import { syncBuiltinESMExports } from "node:module";
import { Writable, Readable } from "node:stream";

export function configureVertexMock(dispatcher) {
  const targets = [];
  if (dispatcher) {
    targets.push(dispatcher);
  } else {
    if (Array.isArray(globalThis.__MECHA_MOCK_AGENTS__)) {
      for (const a of globalThis.__MECHA_MOCK_AGENTS__) {
        if (a && !targets.includes(a)) targets.push(a);
      }
    }
    if (globalThis.__MECHA_PI_MOCK_AGENT__ && !targets.includes(globalThis.__MECHA_PI_MOCK_AGENT__)) targets.push(globalThis.__MECHA_PI_MOCK_AGENT__);
    if (globalThis.__MECHA_HOST_MOCK_AGENT__ && !targets.includes(globalThis.__MECHA_HOST_MOCK_AGENT__)) targets.push(globalThis.__MECHA_HOST_MOCK_AGENT__);
    if (globalThis.__MECHA_MOCK_AGENT__ && !targets.includes(globalThis.__MECHA_MOCK_AGENT__)) targets.push(globalThis.__MECHA_MOCK_AGENT__);
    const gd = getGlobalDispatcher();
    if (gd && !targets.includes(gd)) targets.push(gd);
  }

  const sseResponse =
    `data: ` +
    JSON.stringify({
      responseId: "resp-vertex-synthetic-1",
      candidates: [
        {
          content: {
            parts: [{ text: "synthetic streaming response from vertex adc" }],
          },
          finishReason: "STOP",
        },
      ],
      usageMetadata: {
        promptTokenCount: 10,
        candidatesTokenCount: 15,
        totalTokenCount: 25,
      },
    }) +
    `\n\n`;

  for (const target of targets) {
    if (!target || typeof target.get !== "function") continue;

    const oauthClient = target.get("https://oauth2.googleapis.com");
    if (oauthClient && typeof oauthClient.intercept === "function") {
      oauthClient
        .intercept({ path: "/token", method: "POST" })
        .reply(200, {
          access_token: "synthetic-vertex-access-token",
          expires_in: 3600,
          token_type: "Bearer",
        }, {
          headers: { "content-type": "application/json" },
        })
        .persist();
    }

    for (const host of [
      "https://us-central1-aiplatform.googleapis.com",
      "https://aiplatform.googleapis.com",
    ]) {
      const vertexClient = target.get(host);
      if (vertexClient && typeof vertexClient.intercept === "function") {
        vertexClient
          .intercept({
            path: (p) => p.includes("streamGenerateContent") || p.includes("generateContent"),
            method: "POST",
          })
          .reply(200, sseResponse, {
            headers: {
              "content-type": "text/event-stream; charset=utf-8",
              "cache-control": "no-cache",
            },
          })
          .persist();
      }
    }
  }
}

function isVertexOrOAuthTarget(urlObj, method) {
  const m = (method || "POST").toUpperCase();
  if (urlObj.hostname === "oauth2.googleapis.com") {
    return (urlObj.pathname === "/token" || urlObj.pathname.startsWith("/token?")) && m === "POST";
  }
  if (urlObj.hostname.endsWith("-aiplatform.googleapis.com") || urlObj.hostname === "aiplatform.googleapis.com") {
    return (
      (urlObj.pathname.includes("streamGenerateContent") || urlObj.pathname.includes("generateContent")) &&
      m === "POST"
    );
  }
  return false;
}

function adaptHttpsToMockAgent() {
  const origHttpsRequest = https.request;

  https.request = function (input, options, cb) {
    let callback = cb;
    let reqOptions = {};

    if (typeof input === "string" || input instanceof URL) {
      const u = new URL(input);
      reqOptions = {
        protocol: u.protocol,
        hostname: u.hostname,
        port: u.port,
        path: u.pathname + u.search,
      };
      if (typeof options === "function") {
        callback = options;
      } else if (options && typeof options === "object") {
        Object.assign(reqOptions, options);
      }
    } else if (input && typeof input === "object") {
      reqOptions = Object.assign({}, input);
      if (typeof options === "function") {
        callback = options;
      } else if (options && typeof options === "object") {
        Object.assign(reqOptions, options);
      }
    }

    const host = reqOptions.hostname || reqOptions.host || "localhost";
    const proto = reqOptions.protocol || "https:";
    const pathStr = reqOptions.path || "/";
    let urlObj;
    try {
      urlObj = new URL(pathStr.startsWith("http") ? pathStr : `${proto}//${host}${pathStr}`);
    } catch {
      urlObj = null;
    }

    const method = (reqOptions.method || "POST").toUpperCase();

    if (urlObj && isVertexOrOAuthTarget(urlObj, method)) {
      const chunks = [];
      let aborted = false;

      class FakeClientRequest extends Writable {
        constructor() {
          super();
          this.headers = Object.assign({}, reqOptions.headers);
          this.method = method;
          this.path = urlObj.pathname + urlObj.search;
          this.aborted = false;
        }

        setHeader(name, value) {
          this.headers[name.toLowerCase()] = value;
        }

        getHeader(name) {
          return this.headers[name.toLowerCase()];
        }

        getHeaderNames() {
          return Object.keys(this.headers);
        }

        getHeaders() {
          return Object.assign({}, this.headers);
        }

        hasHeader(name) {
          return Boolean(this.headers[name.toLowerCase()]);
        }

        removeHeader(name) {
          delete this.headers[name.toLowerCase()];
        }

        abort() {
          this.aborted = true;
          aborted = true;
          this.destroy(new Error("Request aborted"));
        }

        setTimeout(ms, cb) {
          if (typeof cb === "function") {
            this.once("timeout", cb);
          }
          return this;
        }

        setNoDelay() {
          return this;
        }

        setSocketKeepAlive() {
          return this;
        }

        _write(chunk, encoding, callback) {
          chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk, encoding));
          callback();
        }

        _final(callback) {
          callback();
          if (aborted) return;
          this._dispatch();
        }

        async _dispatch() {
          const bodyBuf = Buffer.concat(chunks);
          try {
            if (urlObj.hostname === "oauth2.googleapis.com" && (urlObj.pathname === "/token" || urlObj.pathname.startsWith("/token?"))) {
              if (aborted) return;

              const tokenData = JSON.stringify({
                access_token: "synthetic-vertex-access-token",
                expires_in: 3600,
                token_type: "Bearer",
              });

              const resStream = new Readable({
                read() {},
              });
              resStream.statusCode = 200;
              resStream.statusMessage = "OK";
              resStream.headers = {
                "content-type": "application/json; charset=utf-8",
                "content-length": String(Buffer.byteLength(tokenData)),
              };
              resStream.rawHeaders = Object.entries(resStream.headers).flat();
              resStream.httpVersion = "1.1";
              resStream.httpVersionMajor = 1;
              resStream.httpVersionMinor = 1;

              if (typeof callback === "function") {
                callback(resStream);
              }
              this.emit("response", resStream);

              resStream.push(tokenData);
              resStream.push(null);
              return;
            }

            const dispatcher = globalThis.__MECHA_PI_MOCK_AGENT__ || globalThis.__MECHA_HOST_MOCK_AGENT__ || globalThis.__MECHA_MOCK_AGENT__ || getGlobalDispatcher();
            const response = await dispatcher.request({
              origin: urlObj.origin,
              path: urlObj.pathname + urlObj.search,
              method: this.method,
              headers: this.headers,
              body: bodyBuf,
            });

            if (aborted) return;

            const resStream = new Readable({
              read() {},
            });
            resStream.statusCode = response.statusCode;
            resStream.statusMessage = response.statusCode === 200 ? "OK" : "Error";
            resStream.headers = response.headers || {};
            resStream.rawHeaders = Object.entries(resStream.headers).flat();
            resStream.httpVersion = "1.1";
            resStream.httpVersionMajor = 1;
            resStream.httpVersionMinor = 1;

            if (typeof callback === "function") {
              callback(resStream);
            }
            this.emit("response", resStream);

            response.body.on("data", (chunk) => {
              if (!aborted) resStream.push(chunk);
            });
            response.body.on("end", () => {
              if (!aborted) resStream.push(null);
            });
            response.body.on("error", (err) => {
              resStream.emit("error", err);
            });
          } catch (err) {
            if (!aborted) {
              const code = err?.code || err?.cause?.code || err?.cause?.name || err?.name || "ERR_VERTEX_DISPATCH";
              const diag = `[DIAG: code=${code} method=${this.method} origin=${urlObj.origin} path=${urlObj.pathname}]`;
              if (err && (!err.message || !err.message.includes("[DIAG:"))) {
                err.message = `${err.message || ""} ${diag}`.trim();
              }
              this.emit("error", err);
            }
          }
        }
      }

      const clientReq = new FakeClientRequest();
      if (typeof callback === "function") {
        clientReq.on("response", callback);
      }
      return clientReq;
    }

    return origHttpsRequest.apply(this, arguments);
  };

  https.get = function () {
    const req = https.request.apply(this, arguments);
    req.end();
    return req;
  };

  syncBuiltinESMExports();
}

adaptHttpsToMockAgent();
configureVertexMock();
