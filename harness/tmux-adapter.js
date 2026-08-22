import { execFile as execFileCallback } from "node:child_process";
import { promisify } from "node:util";

const execFile = promisify(execFileCallback);

export function createTmuxAdapter({ binary = "tmux", exec = execFile } = {}) {
  async function run(args) {
    try {
      const { stdout } = await exec(binary, args, { maxBuffer: 1024 * 1024 });
      return stdout.trim();
    } catch (error) {
      const wrapped = new Error("tmux operation failed", { cause: error });
      wrapped.code = error.code ?? "tmux_failed";
      wrapped.statusCode = error.code === 1 ? 404 : 500;
      throw wrapped;
    }
  }
  return {
    async enumerate() {
      try {
        const output = await run(["list-sessions", "-F", "#{session_name}"]);
        return output === "" ? [] : output.split("\n");
      } catch (error) {
        // tmux exits 1 when the server has no sessions. That is an empty
        // inventory, not an unavailable tmux server, so provisioning can create
        // the first session.
        if (error.code === 1) return [];
        throw error;
      }
    },
    async enumerateWindows(session) {
      const output = await run(["list-windows", "-t", session, "-F", "#{window_name}"]);
      return output === "" ? [] : output.split("\n").map((name) => ({ name, target: `${session}:${name}` }));
    },
    provisionSession(session, name) { return run(["new-session", "-d", "-s", session, "-n", name]).then(() => ({ session, name })); },
    provision(session, name) { return run(["new-window", "-d", "-t", session, "-n", name]).then(() => ({ name })); },
    remove(session, name) { return run(["kill-window", "-t", `${session}:${name}`]).then(() => undefined); },
    rename(session, name, nextName) { return run(["rename-window", "-t", `${session}:${name}`, nextName]).then(() => ({ name: nextName })); },
    sendKeys(session, name, keys) { return run(["send-keys", "-t", `${session}:${name}`, "-l", keys]).then(() => ({ name, sent: true })); },
    start(session, name, sessionId) {
      const keys = sessionId === undefined ? ["start", "Enter"] : ["start", sessionId, "Enter"];
      return run(["send-keys", "-t", `${session}:${name}`, ...keys]).then(() => ({ name, state: "started", ...(sessionId === undefined ? {} : { session_id: sessionId }) }));
    },
    resume(session, name, sessionId) {
      return run(["send-keys", "-t", `${session}:${name}`, "resume", sessionId, "Enter"])
        .then(() => ({ name, state: "resumed", session_id: sessionId }));
    },
    stop(session, name) { return run(["send-keys", "-t", `${session}:${name}`, "C-c"]).then(() => ({ name, state: "stopped" })); },
    restart(session, name, sessionId) {
      const keys = sessionId === undefined ? ["start", "Enter"] : ["start", sessionId, "Enter"];
      return run(["send-keys", "-t", `${session}:${name}`, "C-c"])
        .then(() => run(["send-keys", "-t", `${session}:${name}`, ...keys]))
        .then(() => ({ name, state: "started", ...(sessionId === undefined ? {} : { session_id: sessionId }) }));
    },
  };
}
