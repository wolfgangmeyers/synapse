import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const root = new URL("../../", import.meta.url);
const load = (path) => readFile(new URL(path, root), "utf8").then(JSON.parse);
const fillBodies = (value) => JSON.parse(JSON.stringify(value)
  .replaceAll("__BODY_4096__", "x".repeat(4096))
  .replaceAll("__BODY_4097__", "x".repeat(4097))
  .replaceAll("__BODY_EMOJI_1024__", "😀".repeat(1024))
  .replaceAll("__BODY_EMOJI_1025__", "😀".repeat(1025))
  .replaceAll("__BODY_65536__", "x".repeat(65536))
  .replaceAll("__BODY_65537__", "x".repeat(65537))
  .replaceAll("__BODY_EMOJI_16384__", "😀".repeat(16384))
  .replaceAll("__BODY_EMOJI_16385__", "😀".repeat(16385))
  .replaceAll("__SUBJECT_257__", "s".repeat(257)));
const envelopeOf = (document) => document.type === "message" ? document.message : document;
const defaultBodyPolicy = { name: "default", max_body_bytes: 4096 };
const bodyFitsUtf8Policy = (document, policy) => Buffer.byteLength(envelopeOf(document).body ?? "", "utf8") <= policy.max_body_bytes;

const schema = await load("protocol/v1/schema.json");
const bodyPolicySchema = await load("protocol/v1/body-size-policy.schema.json");
const clientSchema = await load("protocol/v1/client-frame.schema.json");
const serverSchema = await load("protocol/v1/server-frame.schema.json");
const validFixtures = await load("protocol/v1/fixtures/valid.json");
const rejectedFixtures = await load("protocol/v1/fixtures/rejected.json");
const ajv = new Ajv2020({ allErrors: true, strict: false });
addFormats(ajv);
ajv.addSchema(schema);
const validateEnvelope = ajv.getSchema(schema.$id);
const validateBodyPolicy = ajv.compile(bodyPolicySchema);
const validateClient = ajv.compile(clientSchema);
const validateServer = ajv.compile(serverSchema);

function validateFixture(document, direction) {
  if (direction === "client") return validateClient(document);
  if (direction === "server") return validateServer(document);
  return validateEnvelope(document);
}

function policyFor(fixture) {
  const policy = fixture.body_policy ?? defaultBodyPolicy;
  assert.equal(validateBodyPolicy(policy), true, `${fixture.name}: invalid body-size policy: ${ajv.errorsText(validateBodyPolicy.errors)}`);
  return policy;
}

function assertDuplicateFixtureDocumentsAreValid(fixture) {
  const policy = policyFor(fixture);
  for (const document of fixture.documents) {
    const envelope = fillBodies(document);
    assert.equal(validateEnvelope(envelope), true, `${fixture.name}: ${ajv.errorsText(validateEnvelope.errors)}`);
    assert.equal(bodyFitsUtf8Policy(envelope, policy), true, `${fixture.name}: body exceeds the ${policy.name} UTF-8-byte policy`);
  }
}

test("v1 schemas accept every declared envelope, direction-specific frame, and boundary", () => {
  for (const fixture of validFixtures) {
    const policy = policyFor(fixture);
    if (fixture.documents) {
      for (const document of fixture.documents) {
        assert.equal(validateEnvelope(fillBodies(document)), true, fixture.name);
        assert.equal(bodyFitsUtf8Policy(fillBodies(document), policy), true, fixture.name);
      }
      continue;
    }
    const document = fillBodies(fixture.document);
    assert.equal(validateFixture(document, fixture.direction), true, `${fixture.name}: ${ajv.errorsText(validateFixture.errors)}`);
    assert.equal(bodyFitsUtf8Policy(document, policy), true, `${fixture.name}: body exceeds the ${policy.name} UTF-8-byte policy`);
  }
});

function assertRejected(document, direction, policy, name) {
  assert.equal(validateFixture(document, direction) && bodyFitsUtf8Policy(document, policy), false, name);
}

test("default and configured-producer policies pin exact and over UTF-8 byte boundaries", () => {
  const isUnicodeByteBoundary = (fixture) => fixture.name.endsWith("utf8-bytes") && !fixture.name.includes("characters-and");
  const exact = validFixtures.filter(isUnicodeByteBoundary);
  const over = rejectedFixtures.filter(isUnicodeByteBoundary);
  assert.deepEqual(exact.map((fixture) => fixture.body_policy.name), ["default", "configured-producer"]);
  assert.deepEqual(over.map((fixture) => fixture.body_policy.name), ["default", "configured-producer"]);
  for (const fixture of exact) {
    const document = fillBodies(fixture.document);
    const policy = policyFor(fixture);
    assert.equal(validateFixture(document, fixture.direction), true, fixture.name);
    assert.equal(Buffer.byteLength(envelopeOf(document).body, "utf8"), policy.max_body_bytes, fixture.name);
  }
  for (const fixture of over) {
    const document = fillBodies(fixture.document);
    const policy = policyFor(fixture);
    assert.equal(validateFixture(document, fixture.direction), true, `${fixture.name}: structure must remain valid so the UTF-8 policy rejects it`);
    assert.equal(Buffer.byteLength(envelopeOf(document).body, "utf8"), policy.max_body_bytes + 4, fixture.name);
    assert.equal(bodyFitsUtf8Policy(document, policy), false, fixture.name);
  }
});

test("v1 schemas reject every structural, direction, identifier, and boundary violation", () => {
  for (const fixture of rejectedFixtures) {
    const policy = policyFor(fixture);
    if (fixture.documents) continue; // Duplicate-payload disposition is a durable enqueue rule, not JSON shape.
    const document = fillBodies(fixture.document);
    if (!fixture.reject_missing) assertRejected(document, fixture.direction, policy, fixture.name);
    for (const field of fixture.reject_missing ?? []) {
      const missing = structuredClone(document);
      delete missing[field];
      assertRejected(missing, fixture.direction, policy, `${fixture.name}: missing ${field}`);
    }
    if (fixture.reject_version_mismatch) {
      assertRejected({ ...document, version: "v2" }, fixture.direction, policy, `${fixture.name}: version mismatch`);
    }
  }
});

test("rejection fixtures enumerate every required field and version mismatch for every frame", () => {
  const expected = {
    client: {
      register: ["version", "frame_id", "type", "agent_id", "instance_id"],
      lease_renew: ["version", "frame_id", "type", "lease_id"],
      ack: ["version", "frame_id", "type", "message_id"],
      nack: ["version", "frame_id", "type", "message_id", "code"],
      ping: ["version", "frame_id", "type"],
    },
    server: {
      registered: ["version", "frame_id", "type", "lease_id"],
      message: ["version", "frame_id", "type", "attempt", "message"],
      lease_rejected: ["version", "frame_id", "type", "code", "message", "correlation_id"],
      error: ["version", "frame_id", "type", "code", "message", "correlation_id"],
      pong: ["version", "frame_id", "type"],
    },
  };
  for (const [direction, frames] of Object.entries(expected)) {
    for (const [type, fields] of Object.entries(frames)) {
      const fixture = rejectedFixtures.find((candidate) => candidate.name === `missing-required-${direction}-${type.replaceAll("_", "-")}`);
      assert.ok(fixture, `missing rejection fixture for ${direction} ${type}`);
      assert.deepEqual(fixture.reject_missing, fields, `${fixture.name} required fields`);
      assert.equal(fixture.reject_version_mismatch, true, `${fixture.name} version mismatch`);
    }
  }
});

test("client and server frame schemas reject the opposite direction", () => {
  for (const fixture of validFixtures.filter((fixture) => fixture.direction)) {
    const document = fillBodies(fixture.document);
    const wrongDirection = fixture.direction === "client" ? validateServer : validateClient;
    assert.equal(wrongDirection(document), false, `${fixture.name} must not validate in the opposite frame direction`);
  }
});

test("duplicate fixture dispositions distinguish idempotency from conflict", () => {
  const duplicates = [...validFixtures, ...rejectedFixtures].filter((fixture) => fixture.documents);
  for (const fixture of duplicates) assertDuplicateFixtureDocumentsAreValid(fixture);

  const [same] = validFixtures.filter((fixture) => fixture.outcome === "idempotent");
  const [changed] = rejectedFixtures.filter((fixture) => fixture.outcome === "conflict");
  assert.deepEqual(same.documents[0], same.documents[1]);
  assert.equal(changed.documents[0].id, changed.documents[1].id);
  assert.notDeepEqual(changed.documents[0], changed.documents[1]);
});
