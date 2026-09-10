const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function fromRandomBytes(bytes: Uint8Array): string {
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** Creates an RFC 4122 version 4 ID without weakening HTTP-hosted browser clients. */
export function secureUuid(): string {
  const browserCrypto = globalThis.crypto;
  if (browserCrypto !== undefined && typeof browserCrypto.randomUUID === "function") {
    try {
      const uuid = browserCrypto.randomUUID();
      if (UUID_V4.test(uuid)) return uuid;
    } catch {
      // getRandomValues remains a secure fallback on browsers that restrict randomUUID to HTTPS.
    }
  }
  if (browserCrypto === undefined || typeof browserCrypto.getRandomValues !== "function") {
    throw new Error("Secure UUID generation is unavailable");
  }
  const bytes = new Uint8Array(16);
  browserCrypto.getRandomValues(bytes);
  return fromRandomBytes(bytes);
}
