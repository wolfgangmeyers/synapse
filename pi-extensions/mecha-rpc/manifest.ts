import { createHash } from "node:crypto";

import { Value } from "typebox/value";
import type { TSchema } from "typebox";

export type JsonValue = null | boolean | number | string | JsonValue[] | JsonRecord;
export type JsonRecord = { [key: string]: JsonValue };
export type ParameterSchema = JsonRecord;

export type ManifestTool = {
  capability_id: string;
  name: string;
  definition_id: string;
  definition_sha256: string;
  label: string;
  description: string;
  parameters: ParameterSchema;
  operation_id: string;
  prompt_snippet?: string;
  prompt_guidelines?: string[];
};

export type DesiredManifest = {
  protocol: "mecha.tools/v1";
  revision: number;
  tools: ManifestTool[];
};

const MAX_DEPTH = 8;
const MAX_NODES = 2_048;
const MAX_STRING_BYTES = 8_192;
const MAX_TOOL_TEXT_BYTES = 2_000;
const MAX_TOOLS = 32;
const MIN_TOOLS = 16;
const SAFE_NAME = /^[a-z][a-z0-9_]{0,127}$/;
const SAFE_CAPABILITY_ID = /^[a-z][a-z0-9_]{0,59}$/;
const SHA256 = /^[a-f0-9]{64}$/;
const DEFINITION_ID = /^def_[a-f0-9]{64}$/;

export const BASELINE_TOOL_NAMES = Object.freeze([
  "goal",
  "goal_snooze",
  "mecha_parent_message",
  "mecha_mailbox_send",
  "mecha_request_inline_tool_allowance",
  "mecha_recall",
  "mecha_settled_notifications",
  "mecha_context",
  "mecha_state",
  "mecha_priority",
  "mecha_verification_handoff",
  "mecha_reminder",
  "mecha_worker",
  "mecha_pr",
  "mecha_git",
  "mecha_router",
] as const);

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function byteLength(value: string): number {
  return Buffer.byteLength(value, "utf8");
}

function fail(message: string): never {
  throw new TypeError(`Invalid managed tool manifest: ${message}`);
}

function sortedKeys(record: JsonRecord): string[] {
  return Object.keys(record).sort((left, right) => Buffer.compare(Buffer.from(left, "utf8"), Buffer.from(right, "utf8")));
}

/** RFC 8785-compatible canonicalization for the bounded JSON subset accepted here. */
export function canonicalizeJson(value: JsonValue): string {
  if (value === null || typeof value === "boolean" || typeof value === "string") return JSON.stringify(value);
  if (typeof value === "number") {
    if (!Number.isFinite(value) || !Number.isSafeInteger(value) && !Number.isFinite(value)) {
      throw new TypeError("Canonical JSON accepts finite numbers only");
    }
    return Object.is(value, -0) ? "0" : JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalizeJson).join(",")}]`;
  if (!isRecord(value)) throw new TypeError("Canonical JSON received an unsupported value");
  return `{${sortedKeys(value).map((key) => `${JSON.stringify(key)}:${canonicalizeJson(value[key]!)}`).join(",")}}`;
}

export function hashCanonicalJson(value: JsonValue): string {
  return createHash("sha256").update(canonicalizeJson(value), "utf8").digest("hex");
}

function exactlyKeys(record: JsonRecord, allowed: readonly string[], label: string): void {
  for (const key of Object.keys(record)) if (!allowed.includes(key)) fail(`${label} contains unsupported field ${key}`);
}

function requiredKeys(record: JsonRecord, required: readonly string[], label: string): void {
  for (const key of required) if (!Object.hasOwn(record, key)) fail(`${label} is missing ${key}`);
}

function safeInteger(value: unknown, label: string, minimum: number, maximum: number): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum || value > maximum) {
    fail(`${label} must be an integer from ${minimum} through ${maximum}`);
  }
  return value;
}

function boundedText(value: unknown, label: string, maximum = MAX_TOOL_TEXT_BYTES, allowEmpty = false): string {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0) || byteLength(value) > maximum) fail(`${label} must be bounded text`);
  return value;
}

function safePropertyName(value: string): void {
  if (value === "__proto__" || value === "prototype" || value === "constructor") fail(`unsafe property name ${value}`);
}

function validateEnum(value: unknown, type: "string" | "integer" | "number", minimum: number | undefined, maximum: number | undefined): void {
  if (!Array.isArray(value) || value.length < 2 || value.length > 64) fail("enum must contain 2 through 64 values");
  const canonical = new Set<string>();
  for (const entry of value) {
    if (type === "string") {
      if (typeof entry !== "string" || byteLength(entry) > MAX_STRING_BYTES) fail("string enum value is invalid");
    } else if (typeof entry !== "number" || !Number.isFinite(entry) || (type === "integer" && !Number.isSafeInteger(entry))
      || minimum === undefined || maximum === undefined || entry < minimum || entry > maximum) {
      fail("numeric enum value is invalid");
    }
    const identity = canonicalizeJson(entry as JsonValue);
    if (canonical.has(identity)) fail("enum contains duplicate values");
    canonical.add(identity);
  }
}

function validateSchemaNode(value: unknown, depth: number, counter: { value: number }): ParameterSchema {
  if (!isRecord(value)) fail("schema node must be an object");
  if (depth > MAX_DEPTH) fail("schema depth exceeds eight");
  counter.value++;
  if (counter.value > MAX_NODES) fail("schema node count exceeds 2048");
  const type = value.type;
  if (typeof type !== "string" || !["object", "array", "string", "integer", "number", "boolean"].includes(type)) {
    fail("schema type is unsupported");
  }
  const description = value.description;
  if (description !== undefined) boundedText(description, "schema description", MAX_TOOL_TEXT_BYTES, true);

  if (type === "object") {
    exactlyKeys(value, ["type", "properties", "required", "additionalProperties", "description"], "object schema");
    requiredKeys(value, ["type", "properties", "required", "additionalProperties"], "object schema");
    if (value.additionalProperties !== false || !isRecord(value.properties) || !Array.isArray(value.required)) fail("object schema must be closed");
    const properties = value.properties;
    const names = Object.keys(properties);
    if (names.length > 64) fail("object schema has too many properties");
    for (const name of names) {
      safePropertyName(name);
      validateSchemaNode(properties[name], depth + 1, counter);
    }
    const required = new Set<string>();
    for (const name of value.required) {
      if (typeof name !== "string" || !Object.hasOwn(properties, name) || required.has(name)) fail("object required must be a unique property subset");
      required.add(name);
    }
    return structuredClone(value);
  }

  if (type === "array") {
    exactlyKeys(value, ["type", "items", "minItems", "maxItems", "description"], "array schema");
    requiredKeys(value, ["type", "items", "maxItems"], "array schema");
    const maxItems = safeInteger(value.maxItems, "array maxItems", 0, 256);
    const minItems = value.minItems === undefined ? 0 : safeInteger(value.minItems, "array minItems", 0, maxItems);
    if (minItems > maxItems) fail("array range is reversed");
    validateSchemaNode(value.items, depth + 1, counter);
    return structuredClone(value);
  }

  if (type === "string") {
    exactlyKeys(value, ["type", "minLength", "maxLength", "enum", "description"], "string schema");
    requiredKeys(value, ["type", "maxLength"], "string schema");
    const maximum = safeInteger(value.maxLength, "string maxLength", 0, MAX_STRING_BYTES);
    const minimum = value.minLength === undefined ? 0 : safeInteger(value.minLength, "string minLength", 0, maximum);
    if (minimum > maximum) fail("string range is reversed");
    if (value.enum !== undefined) validateEnum(value.enum, "string", undefined, undefined);
    return structuredClone(value);
  }

  if (type === "integer" || type === "number") {
    exactlyKeys(value, ["type", "minimum", "maximum", "enum", "description"], "numeric schema");
    requiredKeys(value, ["type", "minimum", "maximum"], "numeric schema");
    const minimum = value.minimum;
    const maximum = value.maximum;
    if (typeof minimum !== "number" || typeof maximum !== "number" || !Number.isFinite(minimum) || !Number.isFinite(maximum)
      || !Number.isSafeInteger(minimum) || !Number.isSafeInteger(maximum) || minimum > maximum) {
      fail("numeric range must be finite safe bounds");
    }
    if (value.enum !== undefined) validateEnum(value.enum, type, minimum, maximum);
    return structuredClone(value);
  }

  exactlyKeys(value, ["type", "description"], "boolean schema");
  requiredKeys(value, ["type"], "boolean schema");
  return structuredClone(value);
}

export function validateParametersSchema(value: unknown): ParameterSchema {
  const schema = validateSchemaNode(value, 1, { value: 0 });
  if (schema.type !== "object") fail("parameters root must be an object");
  return schema;
}

function definitionInput(tool: Omit<ManifestTool, "definition_id" | "definition_sha256" | "name">): JsonRecord {
  return {
    capability_id: tool.capability_id,
    label: tool.label,
    description: tool.description,
    parameters: tool.parameters,
    operation_id: tool.operation_id,
    ...(tool.prompt_snippet === undefined ? {} : { prompt_snippet: tool.prompt_snippet }),
    ...(tool.prompt_guidelines === undefined ? {} : { prompt_guidelines: tool.prompt_guidelines }),
  };
}

function validateTool(value: unknown): ManifestTool {
  if (!isRecord(value)) fail("tool must be an object");
  exactlyKeys(value, ["capability_id", "name", "definition_id", "definition_sha256", "label", "description", "parameters", "operation_id", "prompt_snippet", "prompt_guidelines"], "tool");
  requiredKeys(value, ["capability_id", "name", "definition_id", "definition_sha256", "label", "description", "parameters", "operation_id"], "tool");
  const capabilityId = value.capability_id;
  const name = value.name;
  const definitionId = value.definition_id;
  const definitionSha = value.definition_sha256;
  if (typeof capabilityId !== "string" || !SAFE_CAPABILITY_ID.test(capabilityId)) fail("capability_id is invalid");
  if (typeof name !== "string" || !SAFE_NAME.test(name)) fail("tool name is invalid");
  if (typeof definitionId !== "string" || !DEFINITION_ID.test(definitionId)) fail("definition_id is invalid");
  if (typeof definitionSha !== "string" || !SHA256.test(definitionSha) || definitionId !== `def_${definitionSha}`) fail("definition hash is invalid");
  const label = boundedText(value.label, "tool label");
  const description = boundedText(value.description, "tool description");
  const parameters = validateParametersSchema(value.parameters);
  const operationId = boundedText(value.operation_id, "operation_id", 128);
  const promptSnippet = value.prompt_snippet === undefined ? undefined : boundedText(value.prompt_snippet, "prompt_snippet", MAX_TOOL_TEXT_BYTES, true);
  let promptGuidelines: string[] | undefined;
  if (value.prompt_guidelines !== undefined) {
    if (!Array.isArray(value.prompt_guidelines) || value.prompt_guidelines.length > 64) fail("prompt_guidelines is invalid");
    promptGuidelines = value.prompt_guidelines.map((entry) => boundedText(entry, "prompt guideline", MAX_TOOL_TEXT_BYTES, true));
  }
  const result: ManifestTool = {
    capability_id: capabilityId,
    name,
    definition_id: definitionId,
    definition_sha256: definitionSha,
    label,
    description,
    parameters,
    operation_id: operationId,
    ...(promptSnippet === undefined ? {} : { prompt_snippet: promptSnippet }),
    ...(promptGuidelines === undefined ? {} : { prompt_guidelines: promptGuidelines }),
  };
  if (hashCanonicalJson(definitionInput(result)) !== definitionSha) fail(`definition hash mismatch for ${name}`);
  return result;
}

export function validateDesiredManifest(value: unknown, options: { requireBaseline?: boolean } = {}): DesiredManifest {
  if (!isRecord(value)) fail("desired manifest must be an object");
  exactlyKeys(value, ["protocol", "revision", "tools"], "desired manifest");
  requiredKeys(value, ["protocol", "revision", "tools"], "desired manifest");
  if (value.protocol !== "mecha.tools/v1") fail("unsupported desired manifest protocol");
  const revision = safeInteger(value.revision, "manifest revision", 1, Number.MAX_SAFE_INTEGER);
  if (!Array.isArray(value.tools) || value.tools.length < MIN_TOOLS || value.tools.length > MAX_TOOLS) fail("manifest must contain 16 through 32 tools");
  const tools = value.tools.map(validateTool).sort((left, right) => Buffer.compare(Buffer.from(left.capability_id), Buffer.from(right.capability_id)));
  const names = new Set<string>();
  const capabilityIds = new Set<string>();
  const definitions = new Set<string>();
  for (const tool of tools) {
    if (names.has(tool.name) || capabilityIds.has(tool.capability_id) || definitions.has(tool.definition_sha256)) fail("manifest contains duplicate names or definitions");
    names.add(tool.name); capabilityIds.add(tool.capability_id); definitions.add(tool.definition_sha256);
  }
  if (options.requireBaseline === true) {
    if (revision !== 1 || tools.length !== BASELINE_TOOL_NAMES.length) fail("revision one must be the exact baseline");
    for (const name of BASELINE_TOOL_NAMES) if (!names.has(name)) fail(`revision one is missing ${name}`);
  }
  return { protocol: "mecha.tools/v1", revision, tools };
}

function isValidatedTypeBoxSchema(value: ParameterSchema): value is ParameterSchema & TSchema {
  // validateParametersSchema has already reduced this external JSON to TypeBox's
  // supported structural subset before this narrow runtime-validation boundary.
  return typeof value.type === "string";
}

/** Value.Check deliberately validates the captured pre-coercion object, never a converted Pi input. */
export function validateArguments(value: unknown, schema: ParameterSchema): boolean {
  return isValidatedTypeBoxSchema(schema) && Value.Check(schema, value);
}
