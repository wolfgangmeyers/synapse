import { createRequestId, validateCustomMessageDetails, type CustomMessageType, type LegacyCommandDefinition } from "./protocol.js";
import type { JsonRecord } from "./manifest.js";

export type ExplicitCustomMessageAppendOutcome =
  | { kind: "accepted"; entryId: string }
  | { kind: "already_present"; entryId: string };

export type ExplicitHiddenMessage = {
  customType: string;
  content: string;
  display: false;
  details: JsonRecord;
};

export type ExplicitHiddenMessageOptions = { triggerTurn: false; entryId: string; deliverAs?: never };

type PiLike = {
  sendMessage(message: ExplicitHiddenMessage, options: ExplicitHiddenMessageOptions): Promise<ExplicitCustomMessageAppendOutcome>;
  sendMessage(message: JsonRecord, options?: JsonRecord): void | Promise<void>;
};
type CommandContext = { sessionManager?: { getSessionId?: () => string | undefined } };

export async function emitControlRequest(pi: PiLike, request: JsonRecord): Promise<void> {
  validateCustomMessageDetails("mecha.control.request.v1", request);
  await pi.sendMessage({
    customType: "mecha.control.request.v1",
    content: "Router control request queued.",
    display: false,
    details: request,
  }, { triggerTurn: false });
}

/** The command bridge carries raw input verbatim; router handlers own its grammar and effects. */
export async function emitLegacyCommandControl(
  pi: PiLike,
  definition: LegacyCommandDefinition,
  rawArguments: string,
  context: CommandContext,
  runtimeGeneration: string,
  sessionEpoch: number,
  isChildSession: boolean,
): Promise<{ requestId: string }> {
  const requestId = createRequestId();
  const sessionId = context.sessionManager?.getSessionId?.();
  const availability: JsonRecord = {
    session_id: typeof sessionId === "string" ? sessionId : "",
    kind: isChildSession ? "child" : "root",
  };
  await emitControlRequest(pi, {
    protocol: "mecha.control/v1",
    request_id: requestId,
    operation_id: definition.operationId,
    legacy_command: definition.name,
    raw_arguments: rawArguments,
    runtime_generation: runtimeGeneration,
    session_epoch: sessionEpoch,
    availability,
  });
  return { requestId };
}

export async function sendHiddenCustomMessage(
  pi: PiLike,
  customType: CustomMessageType,
  content: string,
  details: JsonRecord,
  options: { deliverAs?: "followUp"; triggerTurn: boolean },
): Promise<void> {
  validateCustomMessageDetails(customType, details);
  await pi.sendMessage({ customType, content, display: false, details }, {
    ...(options.deliverAs === undefined ? {} : { deliverAs: options.deliverAs }),
    triggerTurn: options.triggerTurn,
  });
}

export async function sendHiddenCustomMessageWithEntryId(
  pi: PiLike,
  customType: CustomMessageType,
  content: string,
  details: JsonRecord,
  entryId: string,
): Promise<ExplicitCustomMessageAppendOutcome> {
  validateCustomMessageDetails(customType, details);
  return await pi.sendMessage(
    { customType, content, display: false, details },
    { triggerTurn: false, entryId },
  );
}
