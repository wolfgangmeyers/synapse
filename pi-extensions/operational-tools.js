import { execFile } from "node:child_process";
import { randomUUID } from "node:crypto";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const SUPERVISOR_TOOLS = join(HERE, ".claude", "skills", "supervisor", "tools");
const MAX_TEXT = 4_000;
export const MAX_OPERATIONAL_OUTPUT = 16_384;
export const OPERATIONAL_TRUNCATION_MARKER = "\n[output truncated]";
const MAX_RUNNER_BUFFER = 64 * 1024;
const ID_PATTERN = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,119}$";
const ID = new RegExp(ID_PATTERN);
const TASK_FIELDS = new Set([
  "branch",
  "window",
  "pr",
  "linear",
  "dev_environment",
  "supervisor_reminder_id",
  "worker_self_reminder_ids",
]);
const TERMINAL_PRIORITY_STATES = new Set(["completed", "deferred", "cancelled"]);
const REDACTED_CREDENTIAL = "[redacted credential]";
const SECRET_FIELD_NAMES = new Set([
  "accesstoken",
  "apikey",
  "authorization",
  "authtoken",
  "clientsecret",
  "password",
  "passwd",
  "privatekey",
  "secret",
  "secretaccesskey",
  "token",
]);
const CREDENTIAL_KEY_CONTEXT = new Set(["access", "api", "auth", "client", "encryption", "private", "secret", "signing"]);

class OperationalInputError extends TypeError {}
class OperationalActionError extends Error {}

const SCRIPT_PATHS = Object.freeze({
  stateCreate: join(SUPERVISOR_TOOLS, "state_add.py"),
  stateQuery: join(SUPERVISOR_TOOLS, "state_query.py"),
  stateUpdateField: join(SUPERVISOR_TOOLS, "state_update_field.py"),
  stateUpdateStatus: join(SUPERVISOR_TOOLS, "state_update_status.py"),
  priority: join(SUPERVISOR_TOOLS, "state_priority.py"),
  verificationHandoff: join(SUPERVISOR_TOOLS, "state_verification_handoff.py"),
  workerList: join(SUPERVISOR_TOOLS, "worker_list.py"),
  workerSend: join(SUPERVISOR_TOOLS, "mailbox_send.py"),
  prStatus: join(SUPERVISOR_TOOLS, "pr_status.py"),
  gitStatus: join(SUPERVISOR_TOOLS, "git_task_status.py"),
});

function processOutput(stdout, stderr) {
  return [stdout, stderr]
    .map((value) => String(value ?? "").trim())
    .filter(Boolean)
    .join("\n");
}

function defaultRunFile(file, args, options = {}) {
  return new Promise((resolve, reject) => {
    execFile(file, args, {
      encoding: "utf8",
      timeout: 30_000,
      maxBuffer: MAX_RUNNER_BUFFER,
      ...options,
    }, (error, stdout, stderr) => {
      if (error) {
        reject(new OperationalActionError(processOutput(stdout, stderr) || "Operational script failed."));
        return;
      }
      resolve({ stdout: String(stdout ?? ""), stderr: String(stderr ?? "") });
    });
  });
}

function object(input) {
  if (input === null || typeof input !== "object" || Array.isArray(input)) {
    throw new OperationalInputError("input must be an object");
  }
  return input;
}

function exact(input, allowed, required) {
  object(input);
  const extras = Object.keys(input).filter((key) => !allowed.includes(key));
  if (extras.length > 0) throw new OperationalInputError("unsupported field(s)");
  for (const key of required) {
    if (input[key] === undefined) throw new OperationalInputError(`${key} is required`);
  }
}

function text(value, name, maximum = MAX_TEXT) {
  if (typeof value !== "string" || value.trim().length === 0 || value.length > maximum) {
    throw new OperationalInputError(`${name} must be a non-empty string no longer than ${maximum} characters`);
  }
  return value.trim();
}

function id(value, name = "identifier") {
  const normalized = text(value, name, 120);
  if (!ID.test(normalized)) throw new OperationalInputError(`${name} contains unsupported characters`);
  return normalized;
}

function integer(value, name, minimum, maximum) {
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw new OperationalInputError(`${name} must be an integer from ${minimum} through ${maximum}`);
  }
  return value;
}

function choice(value, name, allowed) {
  if (!allowed.includes(value)) throw new OperationalInputError(`${name} must be one of: ${allowed.join(", ")}`);
  return value;
}

function secretKey(value) {
  const compact = value.replace(/[_-]/g, "").toLowerCase();
  if (SECRET_FIELD_NAMES.has(compact)) return true;
  const parts = value.toLowerCase().split(/[_-]/).filter(Boolean);
  const last = parts.at(-1);
  if (["password", "passwd", "secret", "token"].includes(last)) return true;
  return last === "key" && parts.slice(0, -1).some((part) => CREDENTIAL_KEY_CONTEXT.has(part));
}

function quotedEnd(value, start) {
  const quote = value[start];
  for (let index = start + 1; index < value.length; index += 1) {
    if (value[index] === "\\") {
      index += 1;
      continue;
    }
    if (value[index] === quote) return index + 1;
  }
  return value.length;
}

function decodeQuoted(value) {
  if (value[0] === '"') {
    try {
      return JSON.parse(value);
    } catch {
      // Error output is not guaranteed to be valid JSON. The fallback still lets us inspect
      // ordinary backslash-escaped quotes without trusting a partial parse as a boundary.
    }
  }
  const closed = value.length > 1 && value.at(-1) === value[0];
  const body = value.slice(1, closed ? -1 : undefined);
  return body.replace(/\\(?:u\{([0-9a-fA-F]+)\}|u([0-9a-fA-F]{4})|x([0-9a-fA-F]{2})|([\s\S]))/g,
    (_match, codePoint, unicode, hex, escaped) => {
      if (codePoint !== undefined) return String.fromCodePoint(Number.parseInt(codePoint, 16));
      if (unicode !== undefined) return String.fromCharCode(Number.parseInt(unicode, 16));
      if (hex !== undefined) return String.fromCharCode(Number.parseInt(hex, 16));
      return ({ n: "\n", r: "\r", t: "\t", b: "\b", f: "\f", v: "\v" })[escaped] ?? escaped;
    });
}

function encodeQuoted(value, quote) {
  if (quote === '"') return JSON.stringify(value);
  return `'${value.replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/\r/g, "\\r").replace(/\n/g, "\\n")}'`;
}

function identifierStart(character) {
  return character !== undefined && /[A-Za-z_]/.test(character);
}

function identifierPart(character) {
  return character !== undefined && /[A-Za-z0-9_-]/.test(character);
}

function assignmentBoundary(value, index) {
  return index === 0 || !identifierPart(value[index - 1]);
}

function redactCredentialAssignments(value, depth = 0) {
  if (depth > 8) return "[redacted nested output]";
  let output = "";
  let index = 0;

  const redactValue = (start, key) => {
    let cursor = start;
    while (/\s/.test(value[cursor] ?? "")) cursor += 1;
    const spacing = value.slice(start, cursor);
    if (value[cursor] === '"' || value[cursor] === "'") {
      const end = quotedEnd(value, cursor);
      const closed = value[end - 1] === value[cursor];
      return {
        end,
        replacement: `${spacing}${value[cursor]}${REDACTED_CREDENTIAL}${closed ? value[cursor] : ""}`,
      };
    }
    let end = cursor;
    const authorization = key.replace(/[_-]/g, "").toLowerCase() === "authorization";
    while (end < value.length && (authorization ? !/[\r\n,;]/.test(value[end]) : !/[\s,;\]}]/.test(value[end]))) end += 1;
    return { end, replacement: `${spacing}${REDACTED_CREDENTIAL}` };
  };

  while (index < value.length) {
    const character = value[index];

    if (character === '"' || character === "'") {
      const end = quotedEnd(value, index);
      const raw = value.slice(index, end);
      let separator = end;
      while (/\s/.test(value[separator] ?? "")) separator += 1;
      if ((value[separator] === ":" || value[separator] === "=") && secretKey(decodeQuoted(raw))) {
        const assignmentEnd = separator + 1;
        const redacted = redactValue(assignmentEnd, decodeQuoted(raw));
        output += value.slice(index, assignmentEnd) + redacted.replacement;
        index = redacted.end;
        continue;
      }
      const decoded = decodeQuoted(raw);
      const nested = redactCredentialAssignments(decoded, depth + 1);
      output += nested === decoded ? raw : encodeQuoted(nested, character);
      index = end;
      continue;
    }

    if (character === "-" && value[index + 1] === "-" && assignmentBoundary(value, index)) {
      let nameEnd = index + 2;
      while (identifierPart(value[nameEnd])) nameEnd += 1;
      const name = value.slice(index + 2, nameEnd);
      let separator = nameEnd;
      while (/\s/.test(value[separator] ?? "")) separator += 1;
      if (secretKey(name) && (value[separator] === "=" || separator > nameEnd)) {
        const valueStart = value[separator] === "=" ? separator + 1 : nameEnd;
        const redacted = redactValue(valueStart, name);
        output += value.slice(index, valueStart) + redacted.replacement;
        index = redacted.end;
        continue;
      }
    }

    if (identifierStart(character) && assignmentBoundary(value, index)) {
      let nameEnd = index + 1;
      while (identifierPart(value[nameEnd])) nameEnd += 1;
      const name = value.slice(index, nameEnd);
      let separator = nameEnd;
      while (/\s/.test(value[separator] ?? "")) separator += 1;
      if (secretKey(name) && (value[separator] === ":" || value[separator] === "=")) {
        const valueStart = separator + 1;
        const redacted = redactValue(valueStart, name);
        output += value.slice(index, valueStart) + redacted.replacement;
        index = redacted.end;
        continue;
      }
    }

    output += character;
    index += 1;
  }
  return output;
}

function redactSecrets(value) {
  const structural = redactCredentialAssignments(value);
  return structural
    .replace(/-----BEGIN (?:RSA |OPENSSH |EC )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |OPENSSH |EC )?PRIVATE KEY-----/g, "[redacted private key]")
    .replace(/(https?:\/\/)[^/\s:@]+:[^@\s/]+@/gi, `$1${REDACTED_CREDENTIAL}@`);
}

function outputText(value) {
  if (value === undefined || value === null) return "";
  if (typeof value === "string") return value;
  if (["number", "boolean", "bigint"].includes(typeof value)) return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    throw new OperationalActionError("Operational action returned an unsupported result.");
  }
}

function boundedOutput(value) {
  const normalized = redactSecrets(outputText(value)).trim();
  return normalized.length <= MAX_OPERATIONAL_OUTPUT
    ? normalized
    : `${normalized.slice(0, MAX_OPERATIONAL_OUTPUT)}${OPERATIONAL_TRUNCATION_MARKER}`;
}

function normalizedDetails(value) {
  if (value === undefined || value === null) return {};
  try {
    const serialized = JSON.stringify(value, (key, item) => {
      if (key && secretKey(key)) return REDACTED_CREDENTIAL;
      return typeof item === "string" ? redactSecrets(item) : item;
    });
    if (serialized === undefined) return {};
    if (serialized.length > MAX_OPERATIONAL_OUTPUT) {
      return { output: boundedOutput(serialized), truncated: true };
    }
    const normalized = JSON.parse(serialized);
    return normalized !== null && typeof normalized === "object" && !Array.isArray(normalized)
      ? normalized
      : { value: normalized };
  } catch {
    return {};
  }
}

function success(value, details = {}) {
  return {
    content: [{ type: "text", text: boundedOutput(value) || "Operation completed." }],
    details: normalizedDetails(details),
  };
}

function failure(error) {
  const expected = error instanceof OperationalInputError || error instanceof OperationalActionError;
  return {
    content: [{ type: "text", text: boundedOutput(expected ? error.message : "Operational action failed.") }],
    details: {},
    isError: true,
  };
}

function variant(action, properties = {}, required = []) {
  return {
    type: "object",
    additionalProperties: false,
    required: ["action", ...required],
    properties: {
      action: { const: action },
      ...properties,
    },
  };
}

function descriptor(name, label, variants, handler) {
  return {
    name,
    label,
    description: `${label} through fixed, validated operational actions.`,
    parameters: { oneOf: variants },
    async execute(_toolCallId, input) {
      try {
        return await handler(object(input));
      } catch (error) {
        return failure(error);
      }
    },
  };
}

function validateAction(input, definitions) {
  if (typeof input.action !== "string" || definitions[input.action] === undefined) {
    throw new OperationalInputError("unsupported action");
  }
  const definition = definitions[input.action];
  exact(input, ["action", ...Object.keys(definition.properties)], definition.required);
  return definition;
}

function loopbackRouterUrl(environment) {
  const raw = environment.MECHA_ROUTER_URL || "http://127.0.0.1:8765";
  const parsed = new URL(raw);
  if (!['http:', 'https:'].includes(parsed.protocol)
    || !["127.0.0.1", "::1", "localhost"].includes(parsed.hostname)
    || !["", "/"].includes(parsed.pathname)
    || parsed.search || parsed.hash || parsed.username || parsed.password) {
    throw new OperationalActionError("MECHA_ROUTER_URL must be a loopback HTTP(S) base URL");
  }
  return raw.replace(/\/$/, "");
}

export function createOperationalToolDefinitions({
  runFile = defaultRunFile,
  fetchImpl = globalThis.fetch,
  environment = process.env,
  now = () => new Date(),
  generateId = randomUUID,
} = {}) {
  if (typeof runFile !== "function") throw new TypeError("runFile must be a function");
  if (typeof now !== "function") throw new TypeError("now must be a function");
  if (typeof generateId !== "function") throw new TypeError("generateId must be a function");

  const python = environment.PYTHON || "python3";
  const runScript = async (scriptPath, args = []) => {
    if (!Object.values(SCRIPT_PATHS).includes(scriptPath)) throw new OperationalActionError("operational script is not allowlisted");
    const result = await runFile(python, [scriptPath, ...args], { env: environment });
    return processOutput(result?.stdout, result?.stderr);
  };

  async function request(path, { method = "GET", body } = {}) {
    if (typeof fetchImpl !== "function") throw new OperationalActionError("router HTTP client is unavailable");
    const response = await fetchImpl(`${loopbackRouterUrl(environment)}${path}`, {
      method,
      headers: body === undefined ? undefined : { "content-type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(5_000),
    });
    let payload = {};
    if (response.status !== 204) {
      try {
        payload = await response.json();
      } catch {
        throw new OperationalActionError(`router returned invalid JSON (${response.status})`);
      }
    }
    if (!response.ok) throw new OperationalActionError(payload?.error?.message || `router request failed (${response.status})`);
    return payload;
  }

  const task = { type: "string", minLength: 1, maxLength: 120, pattern: ID_PATTERN };
  const shortText = { type: "string", minLength: 1, maxLength: MAX_TEXT };
  const subject = { type: "string", minLength: 1, maxLength: 200 };
  const priorityStatus = { type: "string", enum: ["active", "blocked", "completed", "deferred", "cancelled"] };
  const taskIds = {
    type: "array",
    minItems: 1,
    maxItems: 50,
    uniqueItems: true,
    items: { type: "integer", minimum: 1 },
  };

  const stateDefinitions = {
    query: { properties: { task, state: { type: "string", enum: ["active", "completed", "abandoned"] } }, required: [] },
    create: {
      properties: {
        alias: task,
        type: task,
        project: task,
        description: shortText,
        purpose: shortText,
        window: task,
        branch: task,
        pr: shortText,
        linear: task,
      },
      required: ["alias", "type", "project", "description"],
    },
    update: {
      properties: {
        task,
        field: { type: "string", enum: ["summary", ...TASK_FIELDS] },
        value: shortText,
      },
      required: ["task", "field", "value"],
    },
  };

  const priorityDefinitions = {
    create: { properties: { priorityId: task, name: shortText, status: priorityStatus, taskIds }, required: ["priorityId", "name", "taskIds"] },
    update: { properties: { priorityId: task, name: shortText, status: priorityStatus, taskIds }, required: ["priorityId", "name", "taskIds"] },
    status: { properties: { priorityId: task, status: priorityStatus, evidence: shortText }, required: ["priorityId", "status"] },
  };

  return [
    descriptor("mecha_state", "Supervisor state", [
      variant("query", stateDefinitions.query.properties),
      variant("create", stateDefinitions.create.properties, stateDefinitions.create.required),
      variant("update", stateDefinitions.update.properties, stateDefinitions.update.required),
    ], async (input) => {
      validateAction(input, stateDefinitions);
      if (input.action === "query") {
        if (input.task === undefined && input.state === undefined) throw new OperationalInputError("query requires task or state");
        const args = [];
        if (input.task !== undefined) args.push("--alias", id(input.task, "task"));
        if (input.state !== undefined) args.push("--state", choice(input.state, "state", ["active", "completed", "abandoned"]));
        return success(await runScript(SCRIPT_PATHS.stateQuery, args));
      }
      if (input.action === "create") {
        const args = [
          "--alias", id(input.alias, "alias"),
          "--type", id(input.type, "type"),
          "--project", id(input.project, "project"),
          "--description", text(input.description, "description"),
        ];
        for (const [field, flag] of [["purpose", "--purpose"], ["window", "--window"], ["branch", "--branch"], ["pr", "--pr"], ["linear", "--linear"]]) {
          if (input[field] !== undefined) args.push(flag, field === "purpose" || field === "pr" ? text(input[field], field) : id(input[field], field));
        }
        return success(await runScript(SCRIPT_PATHS.stateCreate, args));
      }
      const taskId = id(input.task, "task");
      const value = text(input.value, "value");
      if (input.field === "summary") {
        return success(await runScript(SCRIPT_PATHS.stateUpdateStatus, [taskId, value]));
      }
      if (!TASK_FIELDS.has(input.field)) throw new OperationalInputError("unsupported task field");
      return success(await runScript(SCRIPT_PATHS.stateUpdateField, [taskId, input.field, value]));
    }),

    descriptor("mecha_priority", "Supervisor priority", [
      variant("create", priorityDefinitions.create.properties, priorityDefinitions.create.required),
      variant("update", priorityDefinitions.update.properties, priorityDefinitions.update.required),
      variant("status", priorityDefinitions.status.properties, priorityDefinitions.status.required),
    ], async (input) => {
      validateAction(input, priorityDefinitions);
      if (input.action === "status") {
        const priorityId = id(input.priorityId, "priorityId");
        const status = choice(input.status, "status", ["active", "blocked", "completed", "deferred", "cancelled"]);
        const args = ["status", priorityId, status];
        if (TERMINAL_PRIORITY_STATES.has(status)) {
          args.push("--evidence", text(input.evidence, "evidence"));
        } else if (input.evidence !== undefined) {
          throw new OperationalInputError("evidence is only accepted for terminal priority states");
        }
        return success(await runScript(SCRIPT_PATHS.priority, args));
      }
      if (!Array.isArray(input.taskIds) || input.taskIds.length < 1 || input.taskIds.length > 50) {
        throw new OperationalInputError("taskIds must contain 1 through 50 task IDs");
      }
      const validatedTaskIds = input.taskIds.map((taskId) => integer(taskId, "taskId", 1, Number.MAX_SAFE_INTEGER));
      if (new Set(validatedTaskIds).size !== validatedTaskIds.length) throw new OperationalInputError("taskIds must be unique");
      const args = ["upsert", "--id", id(input.priorityId, "priorityId"), "--name", text(input.name, "name")];
      if (input.status !== undefined) args.push("--status", choice(input.status, "status", ["active", "blocked", "completed", "deferred", "cancelled"]));
      for (const taskId of validatedTaskIds) args.push("--task-id", String(taskId));
      return success(await runScript(SCRIPT_PATHS.priority, args));
    }),

    descriptor("mecha_verification_handoff", "Verification handoff", [
      variant("record", {
        task,
        reminderId: task,
        successor: shortText,
        remoteVerified: { type: "string", enum: ["pending", "true", "false"] },
      }, ["task", "reminderId", "successor"]),
    ], async (input) => {
      validateAction(input, {
        record: { properties: { task: true, reminderId: true, successor: true, remoteVerified: true }, required: ["task", "reminderId", "successor"] },
      });
      const args = [id(input.task, "task"), "--reminder-id", id(input.reminderId, "reminderId"), "--successor", text(input.successor, "successor")];
      if (input.remoteVerified !== undefined) args.push("--remote-verified", choice(input.remoteVerified, "remoteVerified", ["pending", "true", "false"]));
      return success(await runScript(SCRIPT_PATHS.verificationHandoff, args));
    }),

    descriptor("mecha_reminder", "Mailbox reminder", [
      variant("list"),
      variant("create", { target: task, message: shortText, subject, delayMinutes: { type: "integer", minimum: 1, maximum: 10_080 } }, ["target", "message", "delayMinutes"]),
      variant("delete", { reminderId: task }, ["reminderId"]),
    ], async (input) => {
      const definitions = {
        list: { properties: {}, required: [] },
        create: { properties: { target: true, message: true, subject: true, delayMinutes: true }, required: ["target", "message", "delayMinutes"] },
        delete: { properties: { reminderId: true }, required: ["reminderId"] },
      };
      validateAction(input, definitions);
      if (input.action === "list") {
        const payload = await request("/v1/reminders");
        return success(JSON.stringify(payload), payload);
      }
      if (input.action === "delete") {
        await request(`/v1/reminders/${encodeURIComponent(id(input.reminderId, "reminderId"))}`, { method: "DELETE" });
        return success("Reminder canceled.");
      }
      const minutes = integer(input.delayMinutes, "delayMinutes", 1, 10_080);
      const created = now();
      if (!(created instanceof Date) || Number.isNaN(created.getTime())) throw new OperationalActionError("Operational clock is unavailable.");
      const payload = {
        id: id(generateId(), "generated reminder ID"),
        target: id(input.target, "target"),
        creator: id(environment.MECHA_AGENT_ID || "pi", "creator"),
        body: text(input.message, "message"),
        created_at: created.toISOString(),
        next_due_at: new Date(created.getTime() + minutes * 60_000).toISOString(),
        interval_ms: null,
      };
      if (input.subject !== undefined) payload.subject = text(input.subject, "subject", 200);
      const result = await request("/v1/reminders", { method: "POST", body: payload });
      return success("Reminder accepted.", result);
    }),

    descriptor("mecha_worker", "Managed worker", [
      variant("list"),
      variant("send", { worker: task, message: shortText, subject }, ["worker", "message"]),
    ], async (input) => {
      const definitions = {
        list: { properties: {}, required: [] },
        send: { properties: { worker: true, message: true, subject: true }, required: ["worker", "message"] },
      };
      validateAction(input, definitions);
      if (input.action === "list") return success(await runScript(SCRIPT_PATHS.workerList));
      const args = ["--to", id(input.worker, "worker"), "--body", text(input.message, "message")];
      if (input.subject !== undefined) args.push("--subject", text(input.subject, "subject", 200));
      return success(await runScript(SCRIPT_PATHS.workerSend, args));
    }),

    descriptor("mecha_pr", "Pull request", [
      variant("status", { pullRequest: { type: "integer", minimum: 1 } }, ["pullRequest"]),
    ], async (input) => {
      validateAction(input, { status: { properties: { pullRequest: true }, required: ["pullRequest"] } });
      const pullRequest = integer(input.pullRequest, "pullRequest", 1, Number.MAX_SAFE_INTEGER);
      const raw = await runScript(SCRIPT_PATHS.prStatus, [String(pullRequest), "--json"]);
      let details;
      try {
        details = JSON.parse(raw);
      } catch {
        throw new OperationalActionError("PR status returned invalid JSON");
      }
      return success(raw, { pull_requests: details });
    }),

    descriptor("mecha_git", "Git task status", [
      variant("status", { task }, ["task"]),
    ], async (input) => {
      validateAction(input, { status: { properties: { task: true }, required: ["task"] } });
      const raw = await runScript(SCRIPT_PATHS.gitStatus, ["--task", id(input.task, "task"), "--json"]);
      let details;
      try {
        details = JSON.parse(raw);
      } catch {
        throw new OperationalActionError("git task status returned invalid JSON");
      }
      return success(raw, { repositories: details });
    }),

    descriptor("mecha_router", "Mailbox router", [
      variant("health"),
      variant("retry", { messageId: task }, ["messageId"]),
      variant("dismiss", { messageId: task }, ["messageId"]),
    ], async (input) => {
      const definitions = {
        health: { properties: {}, required: [] },
        retry: { properties: { messageId: true }, required: ["messageId"] },
        dismiss: { properties: { messageId: true }, required: ["messageId"] },
      };
      validateAction(input, definitions);
      if (input.action === "health") {
        const payload = await request("/v1/health");
        return success(JSON.stringify(payload), payload);
      }
      const path = `/v1/messages/${encodeURIComponent(id(input.messageId, "messageId"))}${input.action === "retry" ? "/retry" : ""}`;
      const payload = await request(path, { method: input.action === "retry" ? "POST" : "DELETE" });
      return success(input.action === "retry" ? "Message retry accepted." : "Message dismissed.", payload);
    }),
  ];
}

export function registerOperationalTools(pi, options) {
  if (pi === undefined || typeof pi.registerTool !== "function") throw new TypeError("pi.registerTool is required");
  const tools = createOperationalToolDefinitions(options);
  for (const tool of tools) pi.registerTool(tool);
  return tools;
}
