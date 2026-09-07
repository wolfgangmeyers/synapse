import { createHash } from "node:crypto";

import { Type } from "typebox";
import type { TSchema } from "typebox";
import { Value } from "typebox/value";

export type JsonValue = null | boolean | number | string | JsonValue[] | JsonRecord;
export type JsonRecord = { [key: string]: JsonValue };
/** A JSON-schema-shaped TypeBox schema constructed from the accepted subset. */
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

/** The generated revision-one contract permits its original short names only for these exact bytes. */
const BASELINE_DEFINITION_SHA256: Readonly<Record<typeof BASELINE_TOOL_NAMES[number], string>> = Object.freeze({
  goal: "7beb0bc57b881d24f980bb721e1deb0805942e87aeb7777a458fc13811f092c6",
  goal_snooze: "c48cde2a484a5b70736fa37e3f1c736977109453f20b84192b7fb27968bd0b27",
  mecha_context: "a6ae2502f76c2b5c1837bddcf8c8613e1f96fade26e7e37c89068a7070b844c8",
  mecha_git: "ea52281c3d5dcb4311c571d3ce326a719545b7ffa74acacbc54b2b1226c9a9a5",
  mecha_mailbox_send: "4eab3aa0a26a699d314653f85e7fc5420eebcb2afa5a87bc48c5e372e9fe53b2",
  mecha_parent_message: "2eb0922dc3c0acad276229837c72ca829bfeee04be4ae0c5522cee464cec0da0",
  mecha_pr: "bccc303b438ef65b2d447fdb3c5d3b3efea62eaab2282c504e2880e0f7643585",
  mecha_priority: "39c8b3b2c1471a4df437783f2766d7980a8efa95680e0f1bc7facc50bfb84e3e",
  mecha_recall: "df134eb5b972ba943c393f0ae5807f71f66f6635fa6afbcdeddcc4b49f30a7e0",
  mecha_reminder: "3ee0aee5630c84883e32383cf3c6789a963dad15cf5bed2ca3f98ff710bff698",
  mecha_request_inline_tool_allowance: "98adf7b9a6f69375931b66c1649c669784bc9fd4fa65f5a93a0cbf8404237214",
  mecha_router: "dbcb6eeafd0f177f732ceb39acd336cc60f0f2b7d3e79855b2eb3a89849df205",
  mecha_settled_notifications: "bdcfd97bf8cec67ce70a641370fcf90affa0ab31a80c7c40ee07d123311b1f1f",
  mecha_state: "15f43e468b451bba3b3f1fd7ed823ef4fc0a3333ec892f226c145bd8c0fe7c02",
  mecha_verification_handoff: "8f723b1630e1d0b4a2c9c862566f640302a4cbed37794511a49469d24e43358d",
  mecha_worker: "95185d3de3e00e657ee57f2ebcbea7aa724e44df5679f6d04d5b44c365f6b2da",
});

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function byteLength(value: string): number {
  return Buffer.byteLength(value, "utf8");
}

function fail(message: string): never {
  throw new TypeError(`Invalid managed tool manifest: ${message}`);
}

function hasLoneSurrogate(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      index++;
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function requireIJsonString(value: string, label: string): void {
  if (hasLoneSurrogate(value)) throw new TypeError(`${label} contains a lone surrogate`);
}

function sortedKeys(record: JsonRecord): string[] {
  // RFC 8785 compares UTF-16 code units, not UTF-8 bytes or locale collation.
  return Object.keys(record).sort((left, right) => left === right ? 0 : left < right ? -1 : 1);
}

/** RFC 8785-compatible canonicalization for the bounded JSON subset accepted here. */
export function canonicalizeJson(value: JsonValue): string {
  if (value === null || typeof value === "boolean") return JSON.stringify(value);
  if (typeof value === "string") {
    requireIJsonString(value, "Canonical JSON string");
    return JSON.stringify(value);
  }
  if (typeof value === "number") {
    if (!Number.isFinite(value)) {
      throw new TypeError("Canonical JSON accepts finite numbers only");
    }
    return Object.is(value, -0) ? "0" : JSON.stringify(value);
  }
  if (Array.isArray(value)) return `[${value.map(canonicalizeJson).join(",")}]`;
  if (!isRecord(value)) throw new TypeError("Canonical JSON received an unsupported value");
  return `{${sortedKeys(value).map((key) => {
    requireIJsonString(key, "Canonical JSON property name");
    return `${JSON.stringify(key)}:${canonicalizeJson(value[key]!)}`;
  }).join(",")}}`;
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
  if (typeof value !== "string" || (!allowEmpty && value.length === 0) || byteLength(value) > maximum || hasLoneSurrogate(value)) fail(`${label} must be bounded text`);
  return value;
}

function safePropertyName(value: string): void {
  if (value === "__proto__" || value === "prototype" || value === "constructor" || hasLoneSurrogate(value)) fail(`unsafe property name ${value}`);
}

function validateEnum(value: unknown, type: "string" | "integer" | "number", minimum: number | undefined, maximum: number | undefined): void {
  if (!Array.isArray(value) || value.length < 2 || value.length > 64) fail("enum must contain 2 through 64 values");
  const canonical = new Set<string>();
  for (const entry of value) {
    if (type === "string") {
      if (typeof entry !== "string" || hasLoneSurrogate(entry) || byteLength(entry) > MAX_STRING_BYTES
        || minimum === undefined || maximum === undefined || entry.length < minimum || entry.length > maximum) {
        fail("string enum value is invalid");
      }
    } else if (typeof entry !== "number" || !Number.isFinite(entry) || (type === "integer" && !Number.isSafeInteger(entry))
      || minimum === undefined || maximum === undefined || entry < minimum || entry > maximum) {
      fail("numeric enum value is invalid");
    }
    const identity = canonicalizeJson(entry as JsonValue);
    if (canonical.has(identity)) fail("enum contains duplicate values");
    canonical.add(identity);
  }
}

function schemaRecord(schema: TSchema): ParameterSchema {
  if (!isRecord(schema)) throw new TypeError("TypeBox constructor returned a non-JSON schema");
  return schema;
}

function schemaOptions(description: unknown): { description?: string } {
  const text = description === undefined ? undefined : boundedText(description, "schema description", MAX_TOOL_TEXT_BYTES, true);
  return text === undefined ? {} : { description: text };
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
  const options = schemaOptions(value.description);

  if (type === "object") {
    exactlyKeys(value, ["type", "properties", "required", "additionalProperties", "description"], "object schema");
    requiredKeys(value, ["type", "properties", "required", "additionalProperties"], "object schema");
    if (value.additionalProperties !== false || !isRecord(value.properties) || !Array.isArray(value.required)) fail("object schema must be closed");
    const properties = value.properties;
    const names = Object.keys(properties);
    if (names.length > 64) fail("object schema has too many properties");
    const builtProperties: Record<string, TSchema> = {};
    for (const name of names) {
      safePropertyName(name);
      builtProperties[name] = validateSchemaNode(properties[name], depth + 1, counter);
    }
    const required = new Set<string>();
    for (const name of value.required) {
      if (typeof name !== "string" || !Object.hasOwn(properties, name) || required.has(name)) fail("object required must be a unique property subset");
      required.add(name);
    }
    return schemaRecord(Type.Object(builtProperties, { ...options, additionalProperties: false, required: [...required] }));
  }

  if (type === "array") {
    exactlyKeys(value, ["type", "items", "minItems", "maxItems", "description"], "array schema");
    requiredKeys(value, ["type", "items", "maxItems"], "array schema");
    const maxItems = safeInteger(value.maxItems, "array maxItems", 0, 256);
    const minItems = value.minItems === undefined ? undefined : safeInteger(value.minItems, "array minItems", 0, maxItems);
    if (minItems !== undefined && minItems > maxItems) fail("array range is reversed");
    return schemaRecord(Type.Array(validateSchemaNode(value.items, depth + 1, counter), {
      ...options,
      maxItems,
      ...(minItems === undefined ? {} : { minItems }),
    }));
  }

  if (type === "string") {
    exactlyKeys(value, ["type", "minLength", "maxLength", "enum", "description"], "string schema");
    requiredKeys(value, ["type", "maxLength"], "string schema");
    const maximum = safeInteger(value.maxLength, "string maxLength", 0, MAX_STRING_BYTES);
    const minimum = value.minLength === undefined ? undefined : safeInteger(value.minLength, "string minLength", 0, maximum);
    if (minimum !== undefined && minimum > maximum) fail("string range is reversed");
    if (value.enum !== undefined) validateEnum(value.enum, "string", minimum ?? 0, maximum);
    return schemaRecord(Type.String({
      ...options,
      maxLength: maximum,
      ...(minimum === undefined ? {} : { minLength: minimum }),
      ...(value.enum === undefined ? {} : { enum: structuredClone(value.enum) }),
    }));
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
    const numericOptions = {
      ...options,
      minimum,
      maximum,
      ...(value.enum === undefined ? {} : { enum: structuredClone(value.enum) }),
    };
    return schemaRecord(type === "integer" ? Type.Integer(numericOptions) : Type.Number(numericOptions));
  }

  exactlyKeys(value, ["type", "description"], "boolean schema");
  requiredKeys(value, ["type"], "boolean schema");
  return schemaRecord(Type.Boolean(options));
}

export function validateParametersSchema(value: unknown): ParameterSchema {
  const schema = validateSchemaNode(value, 1, { value: 0 });
  const record = schemaRecord(schema);
  if (record.type !== "object") fail("parameters root must be an object");
  return schema;
}

export function parameterSchemaJson(schema: ParameterSchema): JsonRecord {
  return schema;
}

function definitionInput(tool: Omit<ManifestTool, "definition_id" | "definition_sha256" | "name">): JsonRecord {
  return {
    capability_id: tool.capability_id,
    label: tool.label,
    description: tool.description,
    parameters: parameterSchemaJson(tool.parameters),
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

export function validateDesiredManifest(value: unknown, options: { requireBaseline?: boolean; requireBaselineCapabilities?: boolean } = {}): DesiredManifest {
  if (!isRecord(value)) fail("desired manifest must be an object");
  exactlyKeys(value, ["protocol", "revision", "tools"], "desired manifest");
  requiredKeys(value, ["protocol", "revision", "tools"], "desired manifest");
  if (value.protocol !== "mecha.tools/v1") fail("unsupported desired manifest protocol");
  const revision = safeInteger(value.revision, "manifest revision", 1, Number.MAX_SAFE_INTEGER);
  if (!Array.isArray(value.tools) || value.tools.length < MIN_TOOLS || value.tools.length > MAX_TOOLS) fail("manifest must contain 16 through 32 tools");
  const tools = value.tools.map(validateTool);
  for (let index = 1; index < tools.length; index++) {
    const previous = tools[index - 1]!;
    const current = tools[index]!;
    if (Buffer.compare(Buffer.from(previous.capability_id, "utf8"), Buffer.from(current.capability_id, "utf8")) >= 0) {
      fail("tools must be ordered by capability_id");
    }
  }
  const names = new Set<string>();
  const capabilityIds = new Set<string>();
  const definitions = new Set<string>();
  for (const tool of tools) {
    if (names.has(tool.name) || capabilityIds.has(tool.capability_id) || definitions.has(tool.definition_sha256)) fail("manifest contains duplicate names or definitions");
    const immutableName = `${tool.capability_id}__${tool.definition_sha256}`;
    const baselineCapability = BASELINE_TOOL_NAMES.find((name) => name === tool.capability_id);
    const originalBaselineName = baselineCapability !== undefined
      && tool.name === baselineCapability
      && tool.definition_sha256 === BASELINE_DEFINITION_SHA256[baselineCapability];
    if (tool.name !== immutableName && !originalBaselineName) {
      fail(`tool name does not match its immutable definition identity: ${tool.capability_id}`);
    }
    names.add(tool.name); capabilityIds.add(tool.capability_id); definitions.add(tool.definition_sha256);
  }
  if (revision === 1 || options.requireBaseline === true) {
    if (revision !== 1 || tools.length !== BASELINE_TOOL_NAMES.length) fail("revision one must be the exact baseline");
    for (const name of BASELINE_TOOL_NAMES) {
      const tool = tools.find((candidate) => candidate.capability_id === name);
      if (tool === undefined || tool.name !== name) fail(`revision one is missing ${name}`);
    }
  }
  if (options.requireBaselineCapabilities === true) {
    for (const name of BASELINE_TOOL_NAMES) if (!capabilityIds.has(name)) fail(`current manifest is missing baseline capability ${name}`);
  }
  return { protocol: "mecha.tools/v1", revision, tools };
}

function isValidatedTypeBoxSchema(value: ParameterSchema): value is ParameterSchema & TSchema {
  // validateParametersSchema only returns schemas from TypeBox constructors.
  return typeof value.type === "string";
}

/** Value.Check deliberately validates the captured pre-coercion object, never a converted Pi input. */
export function validateArguments(value: unknown, schema: ParameterSchema): boolean {
  return isValidatedTypeBoxSchema(schema) && Value.Check(schema, value);
}
