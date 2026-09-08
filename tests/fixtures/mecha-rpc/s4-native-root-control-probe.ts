import {
  changedInstalledRootControlResultForTest,
  duplicateInstalledRootControlResultForTest,
  emitInstalledRootControlForTest,
  inspectInstalledRootControlForTest,
  releaseInstalledRootControlForTest,
} from "../../../pi-extensions/mecha-rpc/index.ts";

type Context = { sessionManager?: { getSessionId?: () => string | undefined } };
type Pi = {
  registerCommand: (name: string, command: { description: string; handler: (argumentsText: string, context: Context) => Promise<unknown> }) => void;
};

/** Test-only no-model command that drives and observes the installed goal definition. */
export default function s4NativeRootControlProbe(pi: Pi): void {
  pi.registerCommand("s4-native-root-control-probe", {
    description: "Test-only S4 root control probe.",
    async handler(argumentsText, context) {
      const sessionId = context.sessionManager?.getSessionId?.();
      if (typeof sessionId !== "string" || sessionId.length === 0) throw new TypeError("S4 probe session is unavailable");
      switch (argumentsText.trim()) {
        case "goal-status":
          return { request_id: await emitInstalledRootControlForTest(sessionId) };
        case "release":
          releaseInstalledRootControlForTest(sessionId);
          return { released: true };
        case "observe":
          return inspectInstalledRootControlForTest(sessionId);
        case "duplicate-append": {
          const outcome = await duplicateInstalledRootControlResultForTest(sessionId);
          return { kind: outcome.kind };
        }
        case "changed-append":
          return changedInstalledRootControlResultForTest(sessionId);
        default:
          throw new TypeError("S4 probe arguments are invalid");
      }
    },
  });
}
