import { writeFile } from "node:fs/promises";

const SERVICE_KEY = Symbol.for("@gotgenes/pi-subagents:service");
const REQUIRED_METHODS = ["spawn", "getRecord", "listAgents", "abort", "steer", "waitForAll", "hasRunning"];
const OUTPUT_PATH = process.env.SENTINEL_NATIVE_PROBE_OUTPUT;

function bodyFreeRecord(record) {
  return record === undefined ? undefined : {
    id: record.id,
    type: record.type,
    status: record.status,
    startedAt: record.startedAt,
    completedAt: record.completedAt,
  };
}

export default function sentinelNativeProbe(pi) {
  pi.on("session_start", async () => {
    if (typeof OUTPUT_PATH !== "string" || OUTPUT_PATH.length === 0) {
      throw new Error("SENTINEL_NATIVE_PROBE_OUTPUT is required");
    }
    const service = globalThis[SERVICE_KEY];
    const methods = Object.fromEntries(REQUIRED_METHODS.map((name) => [name, typeof service?.[name] === "function"]));
    if (Object.values(methods).some((available) => !available)) {
      await writeFile(OUTPUT_PATH, JSON.stringify({ published: false, methods }));
      throw new Error("Pi native subagent service is unavailable");
    }
    const id = service.spawn("general-purpose", "Return exactly NATIVE_PROBE_CHILD_DONE and nothing else.", {
      description: "Native compatibility probe",
      model: "anthropic/claude-opus-5",
      inheritContext: true,
    });
    await writeFile(OUTPUT_PATH, JSON.stringify({ published: true, methods, id, phase: "spawned" }));
    await service.waitForAll();
    await writeFile(OUTPUT_PATH, JSON.stringify({
      published: true, methods, id, phase: "completed", record: bodyFreeRecord(service.getRecord(id)),
    }));
  });
}
