#!/usr/bin/env node
/**
 * Validates the RPC acceptance registry and a candidate-bound release run.
 * The registry is generated from the canonical HTML acceptance matrix so a
 * renamed, missing, or duplicated AC row cannot silently disappear.
 */
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SPEC = resolve(ROOT, "RPC_SPEC.html");
const DEFAULT_REGISTRY = resolve(ROOT, "tests/fixtures/mecha-rpc/acceptance-evidence-v1.json");
const schema = "mecha.rpc.acceptance-evidence/v1";
const runSchema = "mecha.rpc.acceptance-run/v1";

function fail(message) { throw new Error(`RPC acceptance registry: ${message}`); }
function hash(value) { return createHash("sha256").update(value).digest("hex"); }
function clean(fragment) {
  return fragment.replace(/<code>(.*?)<\/code>/gisu, "$1").replace(/<a[^>]*>(.*?)<\/a>/gisu, "$1")
    .replace(/<[^>]+>/gu, "").replace(/&lt;/gu, "<").replace(/&gt;/gu, ">")
    .replace(/&amp;/gu, "&").replace(/&quot;/gu, '"').replace(/\s+/gu, " ").trim();
}
function ownerFor(id) {
  const number = Number(id.slice(3));
  if ((number >= 23 && number <= 26) || number === 86) return "P4-auth-models";
  if (number <= 17 || number === 88) return "P3-supervision";
  if ((number >= 36 && number <= 66) || number === 80) return "P2-router-handlers";
  return "P1-P3-preserved";
}
function evidenceClassFor(id) {
  const number = Number(id.slice(3));
  return (number >= 23 && number <= 26) || number === 86
    ? ["actual-go-router", "installed-pi-0.85.0", "provider-helper-fake", "browser-ui"]
    : ["actual-go-router", "installed-pi-0.85.0"];
}
function proofSymbolsFor(id) {
  if (id === "AC-049") return ["rpc-ac-049-manifest", "rpc-ac-049-installed-pi"];
  if (id === "AC-086") return ["rpc-ac-086-provider-auth", "rpc-ac-086-session-restart"];
  return [`rpc-${id.toLowerCase()}`];
}
function acceptanceRows(html) {
  const rows = [];
  const pattern = /<tr\s+id="ac-(\d{3})"[^>]*>\s*<td>AC-\1<\/td>\s*<td>([\s\S]*?)<\/td>\s*<td>([\s\S]*?)<\/td>\s*<td>([\s\S]*?)<\/td>\s*<\/tr>/giu;
  for (const match of html.matchAll(pattern)) {
    rows.push({ id: `AC-${match[1]}`, setup: clean(match[2]), action: clean(match[3]), predicates: [clean(match[4])] });
  }
  rows.sort((left, right) => left.id.localeCompare(right.id));
  if (rows.length !== 90 || new Set(rows.map((row) => row.id)).size !== 90 || rows.some((row, index) => row.id !== `AC-${String(index + 1).padStart(3, "0")}`)) {
    fail(`canonical spec must expose exactly AC-001 through AC-090 (found ${rows.length})`);
  }
  return rows;
}
async function sourceRows() { return acceptanceRows(await readFile(SPEC, "utf8")); }
async function createRegistry() {
  const spec = await readFile(SPEC, "utf8");
  const rows = acceptanceRows(spec);
  return {
    protocol: "mecha.phase1-acceptance-contract/v1",
    schema,
    required_fork_head: "5b362aaa6b65204ace95c62b5089b6ad5a801b4a",
    required_pi_version: "0.85.0",
    artifact_directory: "/tmp/mecha-rpc-acceptance/<mecha-developer-head>/<run-id>",
    required_prompts: { hidden: ["mecha-bootstrap-v1", "mecha-install-tools-v1", "mecha-control-result-v1", "mecha-mailbox-deliver-v1", "mecha-policy-apply-v1", "mecha-child-snapshot-v1", "mecha-post-compaction-notification-v1"], legacy: ["goal", "goal-lock", "goal-snooze", "goal-snooze-turn-limit", "goal-snooze-budget", "goal-snooze-budget-refill", "mecha-parent-message", "inline-tools", "mecha-reply"] },
    required_custom_message_types: ["mecha.tools.accepted.v1", "mecha.tools.rejected.v1", "mecha.control.request.v1", "mecha.control.result.v1", "mecha.mailbox.appended.v1", "mecha.policy.applied.v1", "mecha.child.snapshot.v1", "mecha.child.delta.v1"],
    required_artifacts: ["stock-pi-0.85-parent-commands.json", "stock-pi-0.85-command-registration-failures.json", "stock-pi-0.85-supplemental-collision.json", "stock-pi-0.85-real-child.json", "stock-pi-0.85-custom-message-types.json", "stock-pi-0.85-ac090-child-factory-lifecycle.json", "stock-pi-0.85-lifecycle-generations.json", "stock-pi-0.85-fresh-generation.json"],
    phase2_exclusions: ["router dispatch and persistence", "managed HTTP and authentication", "mailbox recovery", "browser acceptance and release gates"],
    generated_from: { path: "RPC_SPEC.html", sha256: hash(spec), selection: "tr#ac-001 through tr#ac-090" },
    criteria: rows.map((row) => ({
      ...row,
      owner_phase: ownerFor(row.id),
      artifact_id: `rpc-${row.id.toLowerCase()}`,
      proof_symbols: proofSymbolsFor(row.id),
      evidence_class: evidenceClassFor(row.id),
      browser_required: Number(row.id.slice(3)) >= 18,
      release_required: true,
    })),
    composite_proofs: [
      { id: "AC-049-composite", acceptance_id: "AC-049", proof_symbols: proofSymbolsFor("AC-049"), predicate: "The constituent proofs agree on one canonical revision-one manifest." },
      { id: "AC-086-composite", acceptance_id: "AC-086", proof_symbols: proofSymbolsFor("AC-086"), predicate: "The authenticated catalog revision and managed-agent restart fence are observed in one candidate run." },
    ],
    closure: { required_acceptance_ids: rows.map((row) => row.id), no_skipped_release_blocking_cases: true },
  };
}
function equalJson(left, right) { return JSON.stringify(left) === JSON.stringify(right); }
function requireObject(value, label) { if (value === null || typeof value !== "object" || Array.isArray(value)) fail(`${label} must be an object`); return value; }
function currentCandidate() {
  const git = (directory, args) => execFileSync("git", args, { cwd: directory, encoding: "utf8" }).trim();
  const diff = execFileSync("git", ["diff", "--binary", "HEAD", "--"], { cwd: ROOT, encoding: "utf8" });
  const untracked = git(ROOT, ["ls-files", "--others", "--exclude-standard"]).split("\n").filter(Boolean).sort();
  const packages = resolve(ROOT, "..", "pi-packages");
  return {
    mecha_developer_head: git(ROOT, ["rev-parse", "HEAD"]),
    mecha_developer_tree: git(ROOT, ["rev-parse", "HEAD^{tree}"]),
    working_diff_sha256: hash(diff),
    untracked_paths_sha256: hash(untracked.join("\n")),
    pi_packages_head: git(packages, ["rev-parse", "HEAD"]),
    installed_pi_version: "0.85.0",
  };
}
async function verifyRegistry(registry) {
  const expected = await createRegistry();
  if (registry.schema !== schema) fail("schema is invalid");
  if (!equalJson(registry, expected)) fail("does not match the generated canonical spec registry; run with --write-registry after an intentional spec update");
  return expected;
}
async function verifyRun(registry, run, runPath) {
  requireObject(run, "release run");
  if (run.schema !== runSchema || run.result !== "passed" || run.status !== "passed") fail("release run is not a passed mecha.rpc.acceptance-run/v1 artifact");
  if (typeof run.artifact_directory !== "string" || !run.artifact_directory.startsWith("/tmp/")) fail("release run must declare a /tmp candidate artifact directory");
  const artifactDirectory = resolve(run.artifact_directory);
  if (resolve(runPath) !== resolve(artifactDirectory, "acceptance-run.json")) fail("release run must be read from its declared candidate artifact directory");
  if (!equalJson(run.candidate, currentCandidate())) fail("release run is not bound to the current git head/tree/dirty candidate");
  if (!Array.isArray(run.evidence)) fail("release run has no evidence records");
  const expected = new Map(registry.criteria.map((criterion) => [criterion.id, criterion]));
  const actual = new Map();
  for (const evidence of run.evidence) {
    requireObject(evidence, "evidence record");
    const criterion = expected.get(evidence.acceptance_id);
    if (criterion === undefined) fail("evidence references an unknown acceptance ID");
    if (actual.has(criterion.id)) fail(`${criterion.id} has duplicate evidence`);
    if (evidence.artifact_id !== criterion.artifact_id || evidence.owner_phase !== criterion.owner_phase || evidence.status !== "passed" || !equalJson(evidence.evidence_class, criterion.evidence_class) || !equalJson(evidence.observed_predicates, criterion.predicates)) fail(`${criterion.id} does not match its declared artifact, owner, class, status, or predicates`);
    if (!Array.isArray(evidence.proofs) || evidence.proofs.length !== criterion.proof_symbols.length) fail(`${criterion.id} has an incomplete proof set`);
    const proofs = new Map();
    for (const proof of evidence.proofs) {
      requireObject(proof, `${criterion.id} proof`);
      if (typeof proof.proof_symbol !== "string" || proof.status !== "passed" || !Array.isArray(proof.locations) || proof.locations.length === 0 || proofs.has(proof.proof_symbol)) fail(`${criterion.id} proof is malformed`);
      for (const location of proof.locations) {
        if (typeof location !== "string" || !location.startsWith(`${artifactDirectory}/`)) fail(`${criterion.id} proof location must be in this candidate artifact directory`);
        const bytes = await readFile(location).catch(() => undefined);
        if (bytes === undefined) fail(`${criterion.id} proof location does not exist: ${location}`);
      }
      proofs.set(proof.proof_symbol, proof);
    }
    if (!equalJson([...proofs.keys()].sort(), [...criterion.proof_symbols].sort())) fail(`${criterion.id} proof symbols do not match the registry`);
    actual.set(criterion.id, evidence);
  }
  if (actual.size !== expected.size || [...expected.keys()].some((id) => !actual.has(id))) fail("release run does not close every AC-001 through AC-090 row");
  return { candidate: run.candidate, acceptance_count: actual.size, composite_count: registry.composite_proofs.length };
}

const args = new Set(process.argv.slice(2));
const registryPath = process.env.MECHA_RPC_ACCEPTANCE_REGISTRY ? resolve(process.env.MECHA_RPC_ACCEPTANCE_REGISTRY) : DEFAULT_REGISTRY;
if (args.has("--write-registry")) {
  await writeFile(registryPath, `${JSON.stringify(await createRegistry(), null, 2)}\n`, { mode: 0o600 });
  process.stdout.write(`wrote ${registryPath}\n`);
} else {
  const registry = JSON.parse(await readFile(registryPath, "utf8"));
  await verifyRegistry(registry);
  if (args.has("--run") || process.env.MECHA_RPC_ACCEPTANCE_RUN) {
    const runPath = resolve(process.env.MECHA_RPC_ACCEPTANCE_RUN ?? [...args][[...args].indexOf("--run") + 1] ?? "");
    const run = JSON.parse(await readFile(runPath, "utf8"));
    process.stdout.write(`${JSON.stringify(await verifyRun(registry, run, runPath))}\n`);
  } else {
    process.stdout.write(`${JSON.stringify({ registry: registryPath, acceptance_count: registry.criteria.length, composite_count: registry.composite_proofs.length })}\n`);
  }
}
