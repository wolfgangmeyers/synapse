import { mkdtemp, mkdir, writeFile, readFile, rename, rm, stat } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { randomUUID } from "node:crypto";
import { expect, type Page } from "@playwright/test";
import { workflowHarness, assertLocalEvaluatorDefault, userTurn as composeUserTurn, type WorkflowHarnessOptions } from "./workflow-implicit-goals-harness";

export async function antigravityWorkersHarness(script: object = {}, options: WorkflowHarnessOptions = {}) {
  const root = await mkdtemp(join(tmpdir(), "antigravity-browser-"));
  const scriptPath = join(root, "script.json");
  const recordPath = join(root, "record.jsonl");
  const bin = join(root, "bin");
  await mkdir(bin, { mode: 0o700 });
  await writeFile(scriptPath, JSON.stringify(script), { mode: 0o600 });

  // Mock `agy` executable:
  // - Handles `--version` -> "agy version 1.5.9"
  // - Handles `models` -> standard model list
  // - Handles stream-json turns deterministically
  await writeFile(join(bin, "agy"), `#!${process.execPath}
import { watch, readFileSync, writeFileSync, appendFileSync, existsSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';
import readline from 'node:readline';

const scriptPath = ${JSON.stringify(scriptPath)};
const recordPath = ${JSON.stringify(recordPath)};
const rootDir = ${JSON.stringify(root)};
const argv = process.argv.slice(2);

function record(entry) {
  try {
    appendFileSync(recordPath, JSON.stringify({ pid: process.pid, ...entry, timestamp: new Date().toISOString() }) + '\\n');
  } catch (_) {}
}

record({ kind: 'start', argv });

if (argv.includes('--version')) {
  let scriptData = {};
  try {
    scriptData = JSON.parse(readFileSync(scriptPath, 'utf8'));
  } catch (_) {}
  if (scriptData.auth?.signedOut || scriptData.auth?.failVersion) {
    process.exit(1);
  }
  process.stdout.write('agy version 1.5.9\\n');
  process.exit(0);
}

if (argv.includes('models')) {
  let scriptData = {};
  try {
    scriptData = JSON.parse(readFileSync(scriptPath, 'utf8'));
  } catch (_) {}
  if (scriptData.auth?.signedOut) {
    process.stdout.write('Please sign in using agy auth\\n');
    process.exit(1);
  }
  process.stdout.write('gemini-2.5-pro\\ngemini-2.5-flash\\ngemini-3.8-pro\\ngemini-3.8-flash\\n');
  process.exit(0);
}

// Stream-json turn loop simulation
if (argv.includes('--input-format') && argv.includes('stream-json')) {
  let session = 'session';
  const convIdx = argv.indexOf('--conversation');
  if (convIdx >= 0 && argv[convIdx + 1]) {
    session = argv[convIdx + 1];
  }

  let model = 'gemini-2.5-pro';
  const modelIdx = argv.indexOf('--model');
  if (modelIdx >= 0 && argv[modelIdx + 1]) {
    model = argv[modelIdx + 1];
  }

  let scriptData = {};
  try {
    scriptData = JSON.parse(readFileSync(scriptPath, 'utf8'));
  } catch (_) {}

  // Emit system init event on startup
  const initEvent = {
    type: 'system',
    subtype: 'init',
    session_id: session,
    uuid: randomUUID(),
    model: model,
    capabilities: ['interrupt_receipt_v1']
  };
  process.stdout.write(JSON.stringify(initEvent) + '\\n');

  const rl = readline.createInterface({
    input: process.stdin,
    output: process.stdout,
    terminal: false
  });

  let turnIndex = 0;

  rl.on('line', (line) => {
    if (!line.trim()) return;
    record({ kind: 'stdin', line });

    let parsed;
    try {
      parsed = JSON.parse(line);
    } catch (e) {
      return;
    }

    if (parsed.type === 'control_request') {
      const requestId = parsed.request_id;
      const subtype = parsed.request?.subtype;
      if (subtype === 'interrupt') {
        const resp = {
          type: 'control_response',
          response: {
            subtype: 'success',
            request_id: requestId,
            response: { interrupted: true }
          }
        };
        process.stdout.write(JSON.stringify(resp) + '\\n');
      }
      return;
    }

    if (parsed.type === 'user') {
      const userUUID = parsed.uuid || randomUUID();
      const userContent = parsed.message?.content || [];

      // Echo user message as protocol requires (with isReplay: true so runtime recognizes it as the echo)
      const userEcho = {
        type: 'user',
        uuid: userUUID,
        session_id: session,
        isReplay: true,
        message: {
          role: 'user',
          content: userContent
        }
      };
      process.stdout.write(JSON.stringify(userEcho) + '\\n');

      // Check if custom turn script exists
      const customTurns = scriptData.turns || [];
      const turnScript = customTurns[turnIndex];
      turnIndex++;

      const assistantUUID = randomUUID();
      let waiting = false;
      let turnText = '';
      if (turnScript && Array.isArray(turnScript)) {
        for (const step of turnScript) {
          if (step.wait === 'interrupt') {
            waiting = true;
            break;
          }
          if (step.thinking) {
            const assistantEvent = {
              type: 'assistant',
              uuid: assistantUUID,
              session_id: session,
              message: {
                id: randomUUID(),
                role: 'assistant',
                model: 'gemini-2.5-pro',
                content: [{ type: 'thinking', thinking: step.thinking }]
              }
            };
            process.stdout.write(JSON.stringify(assistantEvent) + '\\n');
          }
          if (step.tool) {
            const assistantEvent = {
              type: 'assistant',
              uuid: assistantUUID,
              session_id: session,
              message: {
                id: randomUUID(),
                role: 'assistant',
                model: 'gemini-2.5-pro',
                content: [{ type: 'tool_use', id: step.tool.id, name: step.tool.name, input: step.tool.input }]
              }
            };
            process.stdout.write(JSON.stringify(assistantEvent) + '\\n');
            // Tool execution sends back a tool_result user event
            const toolResultEvent = {
              type: 'user',
              uuid: randomUUID(),
              session_id: session,
              message: {
                role: 'user',
                content: [{ type: 'tool_result', tool_use_id: step.tool.id, content: step.tool.result ?? 'ok', is_error: false }]
              }
            };
            process.stdout.write(JSON.stringify(toolResultEvent) + '\\n');
          }
          if (step.text) {
            turnText += step.text;
            const assistantEvent = {
              type: 'assistant',
              uuid: assistantUUID,
              session_id: session,
              message: {
                id: randomUUID(),
                role: 'assistant',
                model: 'gemini-2.5-pro',
                content: [{ type: 'text', text: step.text }]
              }
            };
            process.stdout.write(JSON.stringify(assistantEvent) + '\\n');
          }
        }
      } else {
        // Default deterministic response
        turnText = 'Antigravity turn completed successfully.';
        const assistantEvent = {
          type: 'assistant',
          uuid: assistantUUID,
          session_id: session,
          message: {
            id: randomUUID(),
            role: 'assistant',
            model: 'gemini-2.5-pro',
            content: [{ type: 'text', text: turnText }]
          }
        };
        process.stdout.write(JSON.stringify(assistantEvent) + '\\n');
      }

      if (!waiting) {
        const resultEvent = {
          type: 'result',
          subtype: 'success',
          session_id: session,
          uuid: randomUUID(),
          result: turnText,
          num_turns: 1
        };
        process.stdout.write(JSON.stringify(resultEvent) + '\\n');
      }
    }
  });

  // Keep process alive until stdin ends or signal received
  rl.on('close', () => {
    process.exit(0);
  });
}
`, { mode: 0o700 });

  const savedPath = process.env.PATH;
  const savedBinary = process.env.MECHA_AGY_BIN;
  process.env.PATH = `${bin}:${savedPath ?? ""}`;
  process.env.MECHA_AGY_BIN = join(bin, "agy");

  let f: Awaited<ReturnType<typeof workflowHarness>>;
  try {
    f = await workflowHarness(options);
  } catch (error) {
    await rm(root, { recursive: true, force: true });
    throw error;
  } finally {
    if (savedPath === undefined) delete process.env.PATH;
    else process.env.PATH = savedPath;
    if (savedBinary === undefined) delete process.env.MECHA_AGY_BIN;
    else process.env.MECHA_AGY_BIN = savedBinary;
  }

  async function records(): Promise<Record<string, unknown>[]> {
    let raw: string;
    try {
      raw = await readFile(recordPath, "utf8");
    } catch (error: any) {
      if (error?.code === "ENOENT") return [];
      throw error;
    }
    return raw.split("\n").slice(0, -1).filter(Boolean).map(line => JSON.parse(line));
  }

  async function request(page: Page, path: string, body?: object, method = "POST", direct = false) {
    const fixtureAgy = path === "/v1/settings/default-model" && body !== undefined &&
      "model" in body && typeof body.model === "object" && body.model !== null &&
      "provider" in body.model && body.model.provider === "antigravity" &&
      "id" in body.model && typeof body.model.id === "string";
    if (!fixtureAgy) assertLocalEvaluatorDefault(path, body);
    return page.evaluate(async ({ url, body, method }) => {
      const response = await fetch(url, {
        method: body === undefined && method === "POST" ? "GET" : method,
        credentials: "omit",
        headers: { "Content-Type": "application/json", "X-Request-ID": crypto.randomUUID() },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      });
      return { status: response.status, body: await response.json() };
    }, { url: (direct ? f.router.httpUrl : f.proxy.url) + path, body, method });
  }

  async function launch(page: Page, options: object = {}) {
    const name = `antigravity-${randomUUID()}`;
    const response = await request(page, "/v1/managed-agents", {
      name,
      cwd: `${f.router.root}/work`,
      runtime: "embedded",
      model: { provider: "antigravity", id: "gemini-2.5-pro" },
      ...options,
    }, "POST", true);
    expect(response.status, JSON.stringify(response.body)).toBe(202);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    return name;
  }

  const tableRows = (table: string, agent: string) => {
    if (!/^[a-z_]+$/.test(table)) throw Error("invalid fixture table");
    return f.rows(`SELECT * FROM ${table} WHERE agent_id=?`, agent);
  };

  async function idle(agent: string) {
    await expect.poll(() => tableRows("managed_agents", agent)[0]?.activity_state).toBe("idle");
  }

  async function final(page: Page, agent: string, text: string) {
    await expect(page.getByText(text, { exact: true }).first()).toBeVisible();
    await idle(agent);
  }

  async function configure(next: object) {
    await writeFile(scriptPath + ".new", JSON.stringify(next), { mode: 0o600 });
    await rename(scriptPath + ".new", scriptPath);
  }

  async function breakExecutable() {
    await rename(join(bin, "agy"), join(bin, "agy.bak"));
  }

  async function restoreExecutable() {
    await rename(join(bin, "agy.bak"), join(bin, "agy"));
  }

  return {
    ...f,
    fixtureRoot: root,
    configure,
    breakExecutable,
    restoreExecutable,
    records,
    request,
    launch,
    tableRows,
    idle,
    final,
    close: async () => {
      try {
        await f.close();
      } finally {
        await rm(root, { recursive: true, force: true });
      }
    },
  };
}

export type AntigravityWorkersHarness = Awaited<ReturnType<typeof antigravityWorkersHarness>>;
