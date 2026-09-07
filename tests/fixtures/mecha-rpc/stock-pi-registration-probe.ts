type Command = { description: string; handler: (argumentsText: string) => Promise<unknown> | unknown };
type Pi = {
  registerCommand: (name: string, command: Command) => void;
  registerTool: (tool: { name: string; label: string; description: string; parameters: Record<string, unknown>; execute: () => Promise<unknown> }) => void;
};

/** Test-only stock Pi probe: creates a pre-existing registry entry or forces one registration failure. */
export default function stockPiRegistrationProbe(pi: Pi): void {
  const mode = process.env.MECHA_RPC_TEST_PROBE_MODE;
  const name = process.env.MECHA_RPC_TEST_PROBE_NAME;
  if ((mode !== "command" && mode !== "tool") || name === undefined) return;
  if (mode === "command") {
    pi.registerCommand(name, { description: "stock collision probe", handler: () => ({}) });
    return;
  }
  pi.registerTool({
    name,
    label: "Stock collision probe",
    description: "Pre-existing stock Pi tool collision probe.",
    parameters: { type: "object", additionalProperties: false, required: [], properties: {} },
    async execute() { return { content: [{ type: "text", text: "probe" }], details: {} }; },
  });
}
