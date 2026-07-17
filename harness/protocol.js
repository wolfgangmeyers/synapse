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

export function validateServerFrame(frame) {
  if (!hasOnlyUnicodeScalars(frame) || !validateServerSchema(frame)) throw new ProtocolValidationError();
  for (const timestamp of ["expires_at", "delivery_expires_at"]) {
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

export function createEnvelope({ from, to, subject, body }, {
  now = () => new Date(),
  generateId = createNanoId,
} = {}) {
  const envelope = {
    id: generateId(),
    from,
    to,
    ...(subject === undefined ? {} : { subject }),
    body,
    created_at: now().toISOString(),
  };

  return validateEnvelope(envelope);
}
