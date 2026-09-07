import { getSubagentsService } from "../../../../pi-packages/packages/pi-subagents/src/service/service.ts";

type CommandContext = { sessionManager?: { getSessionId?: () => string | undefined } };
type ChildPi = { registerTool: (tool: { name: string; label: string; description: string; parameters: Record<string, unknown>; execute: () => Promise<unknown> }) => void };
type ChildFactoryService = {
  registerChildExtensionV1: (registration: { name: string; factory: (pi: ChildPi) => void }) => () => void;
  spawn: (type: string, prompt: string, options?: { description?: string }) => string;
};
type Pi = {
  on: (name: string, handler: (_event: Record<string, unknown>, context: CommandContext) => void) => void;
  registerCommand: (name: string, command: { description: string; handler: (argumentsText: string, context: CommandContext) => Promise<unknown> }) => void;
  sendMessage: (message: Record<string, unknown>, options?: Record<string, unknown>) => void | Promise<void>;
};

let markerDisposer: (() => void) | undefined;
let registeredInitialMarker = false;

function serviceFor(context: CommandContext): ChildFactoryService {
  const ownerSessionId = context.sessionManager?.getSessionId?.();
  const service = ownerSessionId === undefined ? undefined : getSubagentsService(ownerSessionId);
  if (service === undefined || typeof service.registerChildExtensionV1 !== "function") throw new TypeError("subagent service is unavailable");
  return service;
}

function markerFactory(childPi: ChildPi): void {
  childPi.registerTool({
    name: "phase1_ac090_marker",
    label: "AC-090 marker",
    description: "A test-only child factory marker.",
    parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
    async execute() { return { content: [{ type: "text", text: "marker" }], details: {} }; },
  });
}

async function report(pi: Pi, details: Record<string, unknown>): Promise<void> {
  await pi.sendMessage({ customType: "phase1.ac090.v1", display: false, content: "AC-090 probe", details }, { triggerTurn: false });
}

/** Test-only service probe for duplicate registration, disposer replacement, and owner shutdown release. */
export default function stockPiAc090Probe(pi: Pi): void {
  pi.on("session_start", (_event, context) => {
    if (registeredInitialMarker) return;
    markerDisposer = serviceFor(context).registerChildExtensionV1({ name: "phase1-ac090-marker", factory: markerFactory });
    registeredInitialMarker = true;
  });

  pi.registerCommand("phase1-ac090-duplicate-mecha", {
    description: "Assert the active Mecha child factory has a unique name.",
    async handler(_argumentsText, context) {
      let duplicateRejected = false;
      try {
        serviceFor(context).registerChildExtensionV1({ name: "mecha-rpc-managed-child-v1", factory: markerFactory });
      } catch {
        duplicateRejected = true;
      }
      await report(pi, { action: "duplicate-mecha", duplicate_rejected: duplicateRejected });
      if (!duplicateRejected) throw new TypeError("Mecha child factory duplicate was accepted");
      return { duplicateRejected };
    },
  });
  pi.registerCommand("phase1-ac090-dispose", {
    description: "Dispose the marker child factory.",
    async handler() {
      markerDisposer?.();
      markerDisposer = undefined;
      await report(pi, { action: "dispose" });
      return { disposed: true };
    },
  });
  pi.registerCommand("phase1-ac090-reregister", {
    description: "Register the marker factory again after disposal.",
    async handler(_argumentsText, context) {
      markerDisposer = serviceFor(context).registerChildExtensionV1({ name: "phase1-ac090-marker", factory: markerFactory });
      await report(pi, { action: "reregister" });
      return { registered: true };
    },
  });
  pi.registerCommand("phase1-ac090-spawn", {
    description: "Spawn a real child for marker inspection.",
    async handler(_argumentsText, context) {
      const taskId = serviceFor(context).spawn("general-purpose", "Finish without using tools.", { description: "AC-090 child factory probe" });
      await report(pi, { action: "spawn", task_id: taskId });
      return { taskId };
    },
  });
}
