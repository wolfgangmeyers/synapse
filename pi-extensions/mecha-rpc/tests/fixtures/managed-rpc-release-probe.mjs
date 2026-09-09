// Loaded only by cmd/managed-acceptance-router. The commands below drive
// installed Pi compaction events through the real RPC stream. They are never
// included by mailbox-router or the production managed-process configuration.
import { getSubagentsService } from "@gotgenes/pi-subagents";

const COMPACTION_CONTEXT = "managed release fixture context ".repeat(8_000);
const HOLD_MS = 30_000;

function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }

async function serviceFor(context) {
  const ownerSessionId = context?.sessionManager?.getSessionId?.();
  for (let attempt = 0; attempt < 100; attempt++) {
    const service = ownerSessionId === undefined ? undefined : getSubagentsService(ownerSessionId);
    if (service !== undefined && typeof service.registerChildExtensionV1 === "function") return service;
    await delay(10);
  }
  throw new TypeError("managed release fixture cannot access pi-subagents");
}

async function seedCompactionContext(pi, label) {
  await pi.sendMessage({
    customType: "phase3.managed-rpc.compaction-context.v1",
    display: false,
    content: COMPACTION_CONTEXT,
    details: { fixture: "managed-rpc-release", label },
  }, { triggerTurn: false });
}

function installCompactionFixture(pi, commandName) {
  let nextMode;
  let settleCompaction;
  pi.on("session_compact", async () => {
    settleCompaction?.("completed");
    settleCompaction = undefined;
  });
  pi.on("session_compact_failed", async (event) => {
    settleCompaction?.(event.aborted === true ? "aborted" : "failed");
    settleCompaction = undefined;
  });
  pi.on("session_before_compact", async (event) => {
    const mode = nextMode;
    nextMode = undefined;
    if (mode === undefined) return undefined;
    const firstKeptEntryId = event.preparation?.firstKeptEntryId;
    const tokensBefore = event.preparation?.tokensBefore;
    if (typeof firstKeptEntryId !== "string" || typeof tokensBefore !== "number") {
      throw new TypeError("managed release fixture received an invalid compaction preparation");
    }
    if (mode === "aborted") return { cancel: true };
    if (mode === "failed") {
      // Pi deliberately isolates extension-handler exceptions. Return a hook
      // result whose summary accessor fails instead, so the native compaction
      // path—not the extension runner—emits session_compact_failed.
      return {
        compaction: {
          get summary() { throw new Error("managed release fixture forced compaction failure"); },
          firstKeptEntryId,
          tokensBefore,
          details: { source: "managed-rpc-release-fixture", mode },
        },
      };
    }
    if (mode === "hold") await delay(HOLD_MS);
    if (mode === "child_auto") await delay(5_000);
    return {
      compaction: {
        summary: `Managed release fixture ${mode} compaction.`,
        firstKeptEntryId,
        tokensBefore,
        details: { source: "managed-rpc-release-fixture", mode },
      },
    };
  });

  const run = async (mode, context) => {
    if (typeof context?.compact !== "function") throw new TypeError("managed release fixture lacks the Pi compaction action");
    nextMode = mode;
    await seedCompactionContext(pi, `${commandName}-${mode}`);
    const outcome = await new Promise((resolveOutcome, reject) => {
      settleCompaction = resolveOutcome;
      try { context.compact({ customInstructions: COMPACTION_CONTEXT }); } catch (error) { settleCompaction = undefined; reject(error); }
    });
    if (outcome !== (mode === "success" || mode === "hold" ? "completed" : mode)) {
      throw new Error(`managed release fixture expected ${mode}, got ${outcome}`);
    }
    // session_compact listeners run before AgentSession forwards its native
    // compaction_end event to the managed RPC stdout stream. Let that source
    // frame reach the router before beginning the next matrix transition.
    await delay(100);
    return { mode, outcome };
  };

  pi.registerCommand(commandName, {
    description: "Acceptance-only installed-Pi compaction fixture.",
    async handler(argumentsText, context) {
      const mode = argumentsText.trim();
      if (mode === "matrix") {
        return { runs: [await run("success", context), await run("aborted", context), await run("failed", context)] };
      }
      if (mode === "success" || mode === "aborted" || mode === "failed" || mode === "hold" || mode === "child_auto") return run(mode, context);
      throw new TypeError("managed release fixture mode is invalid");
    },
  });

  return { run };
}

function installChildProbe(pi) {
  // Keep the child fixture on the proven child-extension shape. Its lifecycle
  // runs inside the installed subagent, while root-only commands below drive
  // the parent matrix after router readiness.
  pi.on("agent_start", async () => {
    await seedCompactionContext(pi, "child");
    await pi.compact({ customInstructions: COMPACTION_CONTEXT });
  });
  pi.on("session_before_compact", async (event) => {
    const firstKeptEntryId = event.preparation?.firstKeptEntryId;
    const tokensBefore = event.preparation?.tokensBefore;
    if (typeof firstKeptEntryId !== "string" || typeof tokensBefore !== "number") {
      throw new TypeError("managed release fixture received an invalid child compaction preparation");
    }
    await delay(20_000);
    return {
      compaction: {
        summary: "Managed release fixture compacted its child context.",
        firstKeptEntryId,
        tokensBefore,
        details: { source: "managed-rpc-release-fixture" },
      },
    };
  });
}

export default function managedRpcReleaseProbe(pi) {
  const root = installCompactionFixture(pi, "phase3-fixture");
  pi.on("session_start", async (_event, context) => {
    (await serviceFor(context)).registerChildExtensionV1({
      name: "managed-rpc-release-child-probe",
      factory: installChildProbe,
    });
    // This starts before the router can install its post-readiness receiver.
    // get_state.isCompacting is therefore the real Pi repair source for the
    // missing-start acceptance fixture.
    if (process.env.MECHA_TEST_FIXTURE_MODE === "parent-bootstrap-hold") {
      void root.run("hold", context);
    }
    if (process.env.MECHA_TEST_FIXTURE_MODE === "parent-matrix") {
      // Session-start keeps the fixture independent of model prompt parsing.
      // The delayed task lets the router finish its normal readiness handshake
      // so the following native Pi frames reach the live receiver.
      void (async () => {
        await delay(2_500);
        await root.run("success", context);
        await root.run("aborted", context);
        await root.run("failed", context);
        await root.run("hold", context);
      })();
    }
  });
}
