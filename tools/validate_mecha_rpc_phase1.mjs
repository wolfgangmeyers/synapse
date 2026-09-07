#!/usr/bin/env node

import { randomUUID } from "node:crypto";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

const ROOT = resolve(import.meta.dirname, "..");
const FORK_ROOT = resolve(ROOT, "../pi-packages");
const CONTRACT_PATH = join(ROOT, "tests/fixtures/mecha-rpc/acceptance-evidence-v1.json");
const EXPECTED_FORK_HEAD = "06ed583c720ed0fe6d6cc099b26eeb384795ea39";

function utcNow() {
  return new Date().toISOString();
}

function commandOutput(command, args, cwd = ROOT) {
  return execFileSync(command, args, { cwd, encoding: "utf8" }).trim();
}

function requireCleanSource() {
  const mechaStatus = commandOutput("git", ["status", "--porcelain"]);
  const forkStatus = commandOutput("git", ["status", "--porcelain"], FORK_ROOT);
  if (mechaStatus !== "") throw new Error("mecha-developer must be clean before Phase 1 acceptance evidence is generated");
  if (forkStatus !== "") throw new Error("pi-packages must be clean before Phase 1 acceptance evidence is generated");
  if (commandOutput("git", ["branch", "--show-current"], FORK_ROOT) !== "main") throw new Error("pi-packages must be on main");
}

function readContract() {
  const contract = JSON.parse(commandOutput("node", ["-e", `process.stdout.write(require('node:fs').readFileSync(${JSON.stringify(CONTRACT_PATH)}, 'utf8'))`]));
  if (contract.protocol !== "mecha.phase1-acceptance-contract/v1") throw new Error("unexpected Phase 1 acceptance contract protocol");
  if (contract.required_fork_head !== EXPECTED_FORK_HEAD) throw new Error("Phase 1 acceptance contract does not pin the required fork head");
  return contract;
}

function writeJson(path, value) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

function validateEvidence(runRecordPath, contract) {
  const run = JSON.parse(commandOutput("node", ["-e", `process.stdout.write(require('node:fs').readFileSync(${JSON.stringify(runRecordPath)}, 'utf8'))`]));
  const expectedTypes = contract.required_custom_message_types;
  if (!Array.isArray(expectedTypes) || expectedTypes.length !== 8 || new Set(expectedTypes).size !== 8) {
    throw new Error("acceptance contract must name exactly eight custom message types");
  }
  if (run.protocol !== "mecha.phase1-acceptance-run/v1" || run.phase !== "P1" || run.run?.status !== "passed" || typeof run.run?.finished_at !== "string") {
    throw new Error("unexpected or incomplete Phase 1 acceptance run record");
  }
  if (run.candidate?.pi_subagents_head !== EXPECTED_FORK_HEAD || run.candidate?.installed_pi_version !== contract.required_pi_version) {
    throw new Error("run record does not identify the required fork or installed Pi version");
  }
  if (typeof run.candidate?.mecha_developer_head !== "string" || !/^[a-f0-9]{40}$/.test(run.candidate.mecha_developer_head)) {
    throw new Error("run record has no immutable mecha-developer head");
  }
  if (typeof run.run?.run_id !== "string" || typeof run.run?.started_at !== "string") throw new Error("run record has no run identity");
  if (!Array.isArray(run.commands) || run.commands.length === 0 || run.commands.some((entry) => entry.status !== "passed" || entry.exit_code !== 0)) {
    throw new Error("one or more Phase 1 validation commands did not pass");
  }
  if (JSON.stringify(run.phase2_exclusions) !== JSON.stringify(contract.phase2_exclusions)) throw new Error("Phase 2 exclusions differ from the Phase 1 contract");
  if (!Array.isArray(run.evidence) || run.evidence.length !== contract.required_artifacts.length) {
    throw new Error("run record does not list every native proof location");
  }

  for (const artifactName of contract.required_artifacts) {
    const artifactPath = join(dirname(runRecordPath), artifactName);
    const artifact = JSON.parse(commandOutput("node", ["-e", `process.stdout.write(require('node:fs').readFileSync(${JSON.stringify(artifactPath)}, 'utf8'))`]));
    const listedEvidence = run.evidence.find((entry) => entry?.artifact_id === artifactName);
    if (listedEvidence?.status !== "passed" || listedEvidence?.location !== artifactPath) {
      throw new Error(`run record does not retain the passed location for ${artifactName}`);
    }
    if (artifact.status !== "passed" || artifact.run?.run_id !== run.run.run_id || artifact.run?.started_at !== run.run.started_at) {
      throw new Error(`artifact ${artifactName} is not bound to this passed run`);
    }
    if (artifact.candidate?.mecha_developer_head !== run.candidate.mecha_developer_head
      || artifact.candidate?.pi_subagents_head !== run.candidate.pi_subagents_head
      || artifact.candidate?.installed_pi_version !== run.candidate.installed_pi_version) {
      throw new Error(`artifact ${artifactName} does not match the candidate identity`);
    }
  }

  const customArtifactPath = join(dirname(runRecordPath), "stock-pi-0.85-custom-message-types.json");
  const customArtifact = JSON.parse(commandOutput("node", ["-e", `process.stdout.write(require('node:fs').readFileSync(${JSON.stringify(customArtifactPath)}, 'utf8'))`]));
  const observed = customArtifact.evidence?.observed_custom_message_types;
  if (!Array.isArray(observed) || observed.length !== expectedTypes.length) throw new Error("native artifact does not contain all custom message types");
  const observedTypes = observed.map((entry) => entry?.custom_type);
  if (new Set(observedTypes).size !== expectedTypes.length || expectedTypes.some((type) => !observedTypes.includes(type))
    || observed.some((entry) => !Number.isInteger(entry?.observed_count) || entry.observed_count < 1)) {
    throw new Error("native artifact has incomplete custom message observations");
  }
}

function main() {
  requireCleanSource();
  const contract = readContract();
  const startedAt = utcNow();
  const candidate = {
    mecha_developer_head: commandOutput("git", ["rev-parse", "HEAD"]),
    pi_subagents_fork: "wolfgangmeyers/pi-packages",
    pi_subagents_branch: commandOutput("git", ["branch", "--show-current"], FORK_ROOT),
    pi_subagents_head: commandOutput("git", ["rev-parse", "HEAD"], FORK_ROOT),
    installed_pi_version: commandOutput("pi", ["--version"]),
  };
  if (candidate.pi_subagents_head !== EXPECTED_FORK_HEAD) throw new Error(`expected fork ${EXPECTED_FORK_HEAD}, received ${candidate.pi_subagents_head}`);
  if (candidate.installed_pi_version !== contract.required_pi_version) throw new Error(`expected installed Pi ${contract.required_pi_version}, received ${candidate.installed_pi_version}`);

  const runId = randomUUID();
  const runDirectory = join("/tmp/mecha-rpc-acceptance", candidate.mecha_developer_head, runId);
  const runRecordPath = join(runDirectory, "acceptance-evidence-v1.json");
  const run = {
    protocol: "mecha.phase1-acceptance-run/v1",
    phase: "P1",
    candidate,
    run: { run_id: runId, started_at: startedAt, finished_at: null, status: "running" },
    phase2_exclusions: contract.phase2_exclusions,
    commands: [],
    evidence: [],
  };
  writeJson(runRecordPath, run);

  const childEnv = {
    ...process.env,
    MECHA_RPC_ACCEPTANCE_RUN_DIR: runDirectory,
    MECHA_RPC_ACCEPTANCE_RUN_ID: runId,
    MECHA_RPC_ACCEPTANCE_STARTED_AT: startedAt,
    MECHA_RPC_ACCEPTANCE_MECHA_HEAD: candidate.mecha_developer_head,
    MECHA_RPC_ACCEPTANCE_FORK_HEAD: candidate.pi_subagents_head,
    MECHA_RPC_ACCEPTANCE_PI_VERSION: candidate.installed_pi_version,
  };
  const commands = [
    ["fork-source", "npm", ["run", "check:mecha-rpc:fork"]],
    ["fork-head", "node", ["-e", `if (require('node:child_process').execFileSync('git', ['-C', ${JSON.stringify(FORK_ROOT)}, 'rev-parse', 'HEAD'], {encoding:'utf8'}).trim() !== ${JSON.stringify(EXPECTED_FORK_HEAD)}) process.exit(1)`]],
    ["fork-check", "pnpm", ["--dir", FORK_ROOT, "--filter", "@gotgenes/pi-subagents", "run", "check"]],
    ["fork-lint", "pnpm", ["--dir", FORK_ROOT, "--filter", "@gotgenes/pi-subagents", "run", "lint"]],
    ["fork-test", "pnpm", ["--dir", FORK_ROOT, "--filter", "@gotgenes/pi-subagents", "run", "test"]],
    ["fork-build-types", "pnpm", ["--dir", FORK_ROOT, "--filter", "@gotgenes/pi-subagents", "run", "build:types"]],
    ["fork-verify-public-types", "pnpm", ["--dir", FORK_ROOT, "--filter", "@gotgenes/pi-subagents", "run", "verify:public-types"]],
    ["mecha-contract", "npm", ["run", "test:mecha-rpc:contract"]],
    ["mecha-native", "npm", ["run", "test:mecha-rpc:native"]],
    ["mecha-coverage", "npm", ["--prefix", "pi-extensions/mecha-rpc", "run", "test:coverage"]],
    ["mecha-build", "npm", ["--prefix", "pi-extensions/mecha-rpc", "run", "build"]],
  ];

  try {
    for (const [id, executable, args] of commands) {
      const commandStartedAt = utcNow();
      const result = spawnSync(executable, args, { cwd: ROOT, env: childEnv, encoding: "utf8", maxBuffer: 50 * 1024 * 1024 });
      const output = `${result.stdout ?? ""}${result.stderr ?? ""}`;
      const logPath = join(runDirectory, "commands", `${id}.log`);
      mkdirSync(dirname(logPath), { recursive: true });
      writeFileSync(logPath, output, "utf8");
      if (output !== "") process.stdout.write(output);
      const passed = result.status === 0 && result.error === undefined;
      run.commands.push({ id, command: [executable, ...args].join(" "), started_at: commandStartedAt, finished_at: utcNow(), status: passed ? "passed" : "failed", exit_code: result.status, log: logPath });
      writeJson(runRecordPath, run);
      if (!passed) throw result.error ?? new Error(`${id} exited with ${String(result.status)}`);
    }
    run.evidence = contract.required_artifacts.map((artifactId) => ({
      artifact_id: artifactId,
      location: join(runDirectory, artifactId),
      status: "passed",
    }));
    run.run = { ...run.run, finished_at: utcNow(), status: "passed" };
    writeJson(runRecordPath, run);
    validateEvidence(runRecordPath, contract);
    process.stdout.write(`MECHA_RPC_PHASE1_EVIDENCE=${runRecordPath}\n`);
  } catch (error) {
    run.run = { ...run.run, finished_at: utcNow(), status: "failed" };
    writeJson(runRecordPath, run);
    throw error;
  }
}

main();
