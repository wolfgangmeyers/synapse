import Ajv from "ajv";
import { createHash } from "node:crypto";

import { validateEnvelope } from "./protocol.js";
import {
  kbMaintenanceReminderConfigSchema,
  kbMaintenanceReminderPolicy,
} from "./server_jobs/kb_maintenance_reminder.js";

const MAX_JOB_KEY_LENGTH = 64;
const ajv = new Ajv({ allErrors: true, strict: true });

export class ServerJobRegistryError extends Error {
  constructor(message) {
    super(message);
    this.name = "ServerJobRegistryError";
    this.code = "invalid_server_job_registry";
  }
}

export const registeredServerJobs = createServerJobRegistry([{
  key: "kb-maintenance-reminder",
  scheduleIntervalHours: 6,
  scheduleAnchorHour: 0,
  scheduleMinute: 17,
  scheduleTimeZone: "America/Los_Angeles",
  definitionRevision: "1",
  configSchema: kbMaintenanceReminderConfigSchema,
  config: {
    from: "kb-reminder-cron",
    to: "supervisor",
    subject: "KB maintenance reminder",
    body: "KB maintenance reminder:\n\nCheck whether anything important has happened since the last KB update.\nUse the kb skill to update ~/.mecha-developer/kb/ for durable knowledge:\nmajor decisions, project outcomes, changed operating rules, external-system\ndetails, reusable investigations, and non-obvious lessons.\n\nDo not store transient task state, raw logs, or facts better read directly from\ncurrent code. Prefer updating existing KB topics over creating duplicates.\n",
  },
  handler: kbMaintenanceReminderPolicy,
}]);

const jobsByKey = new Map(registeredServerJobs.map((job) => [job.key, job]));

export function createServerJobRegistry(definitions) {
  if (!Array.isArray(definitions) || definitions.length === 0) {
    throw new ServerJobRegistryError("server job registry must contain static definitions");
  }
  const keys = new Set();
  return Object.freeze(definitions.map((definition) => {
    if (definition === null || typeof definition !== "object" || Array.isArray(definition)) {
      throw new ServerJobRegistryError("server job definition is invalid");
    }
    const {
      key,
      scheduleIntervalHours,
      scheduleAnchorHour,
      scheduleMinute,
      scheduleTimeZone,
      definitionRevision,
      configSchema,
      config,
      handler,
    } = definition;
    if (typeof key !== "string"
      || !/^[a-z][a-z0-9-]*$/.test(key)
      || key.length > MAX_JOB_KEY_LENGTH
      || keys.has(key)) {
      throw new ServerJobRegistryError("server job key is invalid or duplicated");
    }
    if (!Number.isSafeInteger(scheduleIntervalHours)
      || scheduleIntervalHours <= 0
      || 24 % scheduleIntervalHours !== 0) {
      throw new ServerJobRegistryError(`server job ${key} has an invalid schedule interval`);
    }
    if (!Number.isSafeInteger(scheduleAnchorHour)
      || scheduleAnchorHour < 0
      || scheduleAnchorHour >= 24
      || !Number.isSafeInteger(scheduleMinute)
      || scheduleMinute < 0
      || scheduleMinute >= 60
      || !isTimeZone(scheduleTimeZone)) {
      throw new ServerJobRegistryError(`server job ${key} has an invalid wall-clock schedule`);
    }
    if (typeof definitionRevision !== "string"
      || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(definitionRevision)) {
      throw new ServerJobRegistryError(`server job ${key} has an invalid definition revision`);
    }
    if (configSchema === null || typeof configSchema !== "object" || Array.isArray(configSchema)) {
      throw new ServerJobRegistryError(`server job ${key} has no static configuration schema`);
    }
    if (typeof handler !== "function") {
      throw new ServerJobRegistryError(`server job ${key} has no repository handler`);
    }
    let validateConfig;
    try {
      validateConfig = ajv.compile(configSchema);
    } catch (error) {
      throw new ServerJobRegistryError(`server job ${key} has an invalid configuration schema: ${error.message}`);
    }
    if (!validateConfig(config) || !hasValidStaticRoute(config)) {
      throw new ServerJobRegistryError(`server job ${key} has invalid static configuration`);
    }
    keys.add(key);
    return Object.freeze({
      key,
      scheduleIntervalHours,
      scheduleAnchorHour,
      scheduleMinute,
      scheduleTimeZone,
      definitionRevision,
      definitionFingerprint: staticDefinitionFingerprint({
        key,
        scheduleIntervalHours,
        scheduleAnchorHour,
        scheduleMinute,
        scheduleTimeZone,
        definitionRevision,
        config,
      }),
      config: deepFreeze(structuredClone(config)),
      handler,
    });
  }));
}

export function serverJobDefinitionFor(key) {
  const job = jobsByKey.get(key);
  if (job === undefined) {
    throw new ServerJobRegistryError(`unknown registered server job ${key}`);
  }
  return job;
}

export function serverJobEnvelope({ key, scheduledFor, now }) {
  const job = serverJobDefinitionFor(key);
  return serverJobEnvelopeForDefinition(job, { scheduledFor, now });
}

export function serverJobEnvelopeForDefinition(job, { scheduledFor, now }) {
  if (job === null || typeof job !== "object" || typeof job.key !== "string") {
    throw new ServerJobRegistryError("server job definition is invalid");
  }
  if (!isTimestamp(scheduledFor) || !isTimestamp(now)) {
    throw new ServerJobRegistryError(`server job ${job.key} received an invalid scheduler timestamp`);
  }
  const policy = job.handler({ config: job.config, scheduledFor });
  if (policy === null || typeof policy !== "object" || Array.isArray(policy)) {
    throw new ServerJobRegistryError(`server job ${job.key} returned invalid message policy`);
  }
  const policyKeys = new Set(["subject", "body"]);
  if (Object.keys(policy).some((key) => !policyKeys.has(key))) {
    throw new ServerJobRegistryError(`server job ${job.key} returned non-policy message fields`);
  }
  const envelope = {
    id: `server-job-${job.key}-${Date.parse(scheduledFor)}`,
    from: job.config.from,
    to: job.config.to,
    ...(policy.subject === undefined ? {} : { subject: policy.subject }),
    body: policy.body,
    created_at: now,
  };
  try {
    return validateEnvelope(envelope);
  } catch (error) {
    throw new ServerJobRegistryError(`server job ${job.key} returned invalid message policy: ${error.message}`);
  }
}

function isTimestamp(value) {
  return typeof value === "string" && Number.isFinite(Date.parse(value));
}

function isTimeZone(value) {
  if (typeof value !== "string" || value.length === 0) return false;
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: value });
    return true;
  } catch {
    return false;
  }
}

function hasValidStaticRoute(config) {
  try {
    validateEnvelope({
      id: "server-job-static-route",
      from: config?.from,
      to: config?.to,
      body: "",
      created_at: "2030-01-01T00:00:00.000Z",
    });
    return true;
  } catch {
    return false;
  }
}

function deepFreeze(value) {
  if (value !== null && typeof value === "object") {
    for (const child of Object.values(value)) deepFreeze(child);
    Object.freeze(value);
  }
  return value;
}

function staticDefinitionFingerprint(definition) {
  return createHash("sha256")
    .update(JSON.stringify(canonicalize(definition)))
    .digest("hex");
}

function canonicalize(value) {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(Object.keys(value).sort().map((key) => [key, canonicalize(value[key])]));
  }
  return value;
}
