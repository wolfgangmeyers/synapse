import { readFile } from "node:fs/promises";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

import { createNanoId } from "./id.js";

const schemaUrl = new URL("../protocol/v1/schema.json", import.meta.url);
const schema = JSON.parse(await readFile(schemaUrl, "utf8"));
const ajv = new Ajv2020({ allErrors: true, strict: false });

addFormats(ajv);

const validateSchema = ajv.compile(schema);
const validateClientSchema = ajv.compile({
  $ref: `${schema.$id}#/$defs/clientFrame`,
});
const validateServerSchema = ajv.compile({
  $ref: `${schema.$id}#/$defs/serverFrame`,
});
const validateLeaseIdentitySchema = ajv.compile({
  $ref: `${schema.$id}#/$defs/leaseIdentity`,
});
const validateDeliveryIdentitySchema = ajv.compile({
  $ref: `${schema.$id}#/$defs/deliveryIdentity`,
});
export const DEFAULT_MAX_BODY_BYTES = 4 * 1024;
export const ABSOLUTE_MAX_BODY_BYTES = 64 * 1024;

export class ProtocolValidationError extends Error {
  constructor(code = "invalid_request") {
    super("invalid mailbox protocol payload");
    this.name = "ProtocolValidationError";
    this.code = code;
  }
}

export function validateEnvelope(envelope, { maxBodyBytes = DEFAULT_MAX_BODY_BYTES } = {}) {
  if (!Number.isSafeInteger(maxBodyBytes)
    || maxBodyBytes <= 0
    || maxBodyBytes > ABSOLUTE_MAX_BODY_BYTES) {
    throw new TypeError("maxBodyBytes must be a positive safe integer no greater than 65536");
  }
  if (!hasOnlyUnicodeScalars(envelope) || !validateSchema(envelope)) throw new ProtocolValidationError();
  if (Number.isNaN(Date.parse(envelope.created_at))) {
    throw new ProtocolValidationError();
  }
  if (Buffer.byteLength(envelope.body, "utf8") > maxBodyBytes) {
    throw new ProtocolValidationError("payload_too_large");
  }

  return envelope;
}

export function validateClientFrame(frame) {
  if (!hasOnlyUnicodeScalars(frame) || !validateClientSchema(frame)) throw new ProtocolValidationError();
  return frame;
}

// Client frames are sent by independently upgraded extensions. Keep strict
// validation for every known field, but remove fields this router version does
// not understand before validation so an additive client change cannot evict a
// healthy connection. Server frames remain strictly validated as before.
export function stripUnknownClientFrameFields(frame) {
  if (frame === null || typeof frame !== "object" || Array.isArray(frame)) return frame;
  const candidates = schema.$defs.clientFrame.oneOf.filter((candidate) => {
    const resolved = resolveSchema(candidate);
    return resolved?.properties?.type?.const === undefined
      || resolved.properties.type.const === frame.type;
  });
  return sanitizeClientValue(frame, candidates.length === 0 ? schema.$defs.clientFrame.oneOf : candidates);
}

function resolveSchema(definition) {
  if (definition?.$ref?.startsWith("#/$defs/")) {
    return schema.$defs[definition.$ref.slice("#/$defs/".length)];
  }
  return definition;
}

function schemaBranches(definitions, value) {
  const branches = [];
  for (const definition of definitions) {
    const resolved = resolveSchema(definition);
    if (Array.isArray(resolved?.oneOf)) {
      // Keep the base properties declared next to a conditional oneOf. Goal
      // result frames declare their payload there and use oneOf only for the
      // accepted/error relationship.
      branches.push(resolved);
      branches.push(...schemaBranches(resolved.oneOf, value));
      continue;
    }
    if (Array.isArray(resolved?.allOf)) {
      // Keep the base object properties alongside conditional branches. The
      // goal projection/state schemas put their required fields on the base
      // schema and only repeat conditional constraints in allOf.
      branches.push(resolved);
      branches.push(...schemaBranches(resolved.allOf, value));
      continue;
    }
    branches.push(resolved);
  }
  const matching = branches.filter((branch) => branch?.properties?.type?.const === undefined
    || branch.properties.type.const === value?.type);
  return matching.length === 0 ? branches : matching;
}

function sanitizeClientValue(value, definitions) {
  if (value === null || typeof value !== "object") return value;
  const branches = schemaBranches(definitions, value);
  if (Array.isArray(value)) {
    const itemSchemas = branches.map((branch) => branch?.items).filter(Boolean);
    return itemSchemas.length === 0
      ? []
      : value.map((item) => sanitizeClientValue(item, itemSchemas));
  }

  const properties = new Map();
  for (const branch of branches) {
    for (const [key, property] of Object.entries(branch?.properties ?? {})) {
      const existing = properties.get(key) ?? [];
      existing.push(property);
      properties.set(key, existing);
    }
  }
  const sanitized = {};
  for (const [key, member] of Object.entries(value)) {
    const propertySchemas = properties.get(key);
    if (propertySchemas !== undefined) {
      sanitized[key] = sanitizeClientValue(member, propertySchemas);
    }
  }
  return sanitized;
}

export function validateServerFrame(frame) {
  if (!hasOnlyUnicodeScalars(frame) || !validateServerSchema(frame)) throw new ProtocolValidationError();
  for (const timestamp of ["expires_at", "delivery_expires_at", "generated_at", "created_at"]) {
    if (timestamp in frame && Number.isNaN(Date.parse(frame[timestamp]))) {
      throw new ProtocolValidationError();
    }
  }
  return frame;
}

export function validateLeaseIdentity(identity) {
  if (!validateLeaseIdentitySchema(identity)) throw new ProtocolValidationError();
  return identity;
}

export function validateDeliveryIdentity(identity) {
  if (!validateDeliveryIdentitySchema(identity)) throw new ProtocolValidationError();
  return identity;
}

function hasOnlyUnicodeScalars(value) {
  if (typeof value === "string") return hasOnlyPairedSurrogates(value);
  if (value === null || typeof value !== "object") return true;
  if (Array.isArray(value)) return value.every(hasOnlyUnicodeScalars);
  return Object.entries(value).every(([key, member]) => (
    hasOnlyPairedSurrogates(key) && hasOnlyUnicodeScalars(member)
  ));
}

function hasOnlyPairedSurrogates(value) {
  for (let index = 0; index < value.length; index += 1) {
    const codeUnit = value.charCodeAt(index);
    if (codeUnit >= 0xd800 && codeUnit <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
      index += 1;
    } else if (codeUnit >= 0xdc00 && codeUnit <= 0xdfff) {
      return false;
    }
  }
  return true;
}

export function createEnvelope({ from, to, subject, body, sentinel }, {
  now = () => new Date(),
  generateId = createNanoId,
} = {}) {
  const envelope = {
    id: generateId(),
    from,
    to,
    ...(subject === undefined ? {} : { subject }),
    ...(sentinel === undefined ? {} : { sentinel }),
    body,
    created_at: now().toISOString(),
  };

  return validateEnvelope(envelope);
}
