type CommandContext = {
  newSession: () => Promise<{ cancelled: boolean }>;
  switchSession: (sessionPath: string) => Promise<{ cancelled: boolean }>;
  fork: (entryId: string) => Promise<{ cancelled: boolean }>;
  reload: () => Promise<void>;
};
type Pi = {
  registerCommand: (name: string, command: {
    description: string;
    handler: (argumentsText: string, context: CommandContext) => Promise<unknown>;
  }) => void;
};

/** Test-only command that changes the process fence immediately before a real Pi lifecycle transition. */
export default function stockPiTransitionProbe(pi: Pi): void {
  pi.registerCommand("phase1-transition", {
    description: "Test probe for a fresh managed runtime generation.",
    async handler(argumentsText, context) {
      const [transition, generation, target] = argumentsText.trim().split(/\s+/, 3);
      if (generation === undefined || !/^[0-9a-f-]{36}$/i.test(generation)) throw new TypeError("transition generation is invalid");
      process.env.MECHA_RPC_RUNTIME_GENERATION = generation;
      if (transition === "new") return context.newSession();
      if (transition === "switch" && target !== undefined) return context.switchSession(target);
      if (transition === "fork" && target !== undefined) return context.fork(target);
      if (transition === "reload") {
        await context.reload();
        return { cancelled: false };
      }
      throw new TypeError("transition target is invalid");
    },
  });
}
