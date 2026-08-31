export interface ProtocolError {
  code: "invalid_protocol_value";
  path: string;
  message: string;
}

export class ProtocolValidationError extends Error {
  public constructor(message: string) {
    super(message);
    this.name = "ProtocolValidationError";
  }
}
