type AppendOutcome = { kind: "accepted" | "already_present"; entryId: string };
type ExplicitOptions = { triggerTurn: false; entryId: string; deliverAs?: never };
type Pi = {
  registerCommand: (name: string, command: { description: string; handler: (argumentsText: string) => Promise<unknown> }) => void;
  sendMessage(message: { customType: string; content: string; display: false; details: Record<string, unknown> }, options: ExplicitOptions): Promise<AppendOutcome>;
  sendMessage(message: Record<string, unknown>, options?: Record<string, unknown>): void | Promise<void>;
};

const CUSTOM_TYPE = "mecha.control.result.v1";
const DETAILS = { protocol: "g4.probe/v1", request_id: "probe-request", result_id: "probe-result" };

/** Test-only direct Pi seam probe. It never uses root correlation or mecha-control-result-v1. */
export default function g4ExplicitEntryIdProbe(pi: Pi): void {
  pi.registerCommand("g4-explicit-entry-id-probe", {
    description: "Test-only explicit custom-message entry ID probe.",
    async handler(argumentsText) {
      const [entryId, mode] = argumentsText.trim().split(/\s+/, 2);
      if (entryId === undefined || mode === undefined || !/^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$/.test(entryId)) {
        throw new TypeError("probe entry ID is invalid");
      }
      const message = {
        customType: CUSTOM_TYPE,
        content: mode === "changed" ? "Changed probe content." : "Direct Pi seam probe.",
        display: false as const,
        details: mode === "changed" ? { ...DETAILS, result_id: "changed-result" } : DETAILS,
      };
      let outcome: AppendOutcome | undefined;
      let rejected = false;
      try {
        outcome = await pi.sendMessage(message, { triggerTurn: false, entryId });
      } catch {
        rejected = true;
      }
      await pi.sendMessage({
        customType: "g4.explicit-entry-id-probe.v1",
        content: "Explicit Pi seam probe completed.",
        display: false,
        details: {
          protocol: "g4.probe/v1",
          entry_id: entryId,
          mode,
          outcome_kind: outcome?.kind,
          outcome_entry_id: outcome?.entryId,
          rejected,
        },
      }, { triggerTurn: false });
      if (mode !== "changed" && outcome === undefined) throw new TypeError("probe append did not return an outcome");
      if (mode === "changed" && !rejected) throw new TypeError("changed probe collision was accepted");
      return { outcome, rejected };
    },
  });
}
