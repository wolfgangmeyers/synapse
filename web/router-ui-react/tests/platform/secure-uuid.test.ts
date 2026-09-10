import { afterEach, describe, expect, it, vi } from "vitest";
import { secureUuid } from "../../src/platform/secure-uuid";

afterEach(() => vi.unstubAllGlobals());

describe("secureUuid", () => {
  it("uses the browser randomUUID implementation when available", () => {
    const randomUUID = vi.fn(() => "46c9ee64-eb71-4383-86e8-7a718dde90b0");
    vi.stubGlobal("crypto", { randomUUID, getRandomValues: vi.fn() });

    expect(secureUuid()).toBe("46c9ee64-eb71-4383-86e8-7a718dde90b0");
    expect(randomUUID).toHaveBeenCalledOnce();
  });

  it("falls back to secure random bytes when randomUUID throws", () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => {
      bytes.fill(0x22);
      return bytes;
    });
    vi.stubGlobal("crypto", { randomUUID: vi.fn(() => { throw new Error("HTTPS required"); }), getRandomValues });

    expect(secureUuid()).toBe("22222222-2222-4222-a222-222222222222");
    expect(getRandomValues).toHaveBeenCalledOnce();
  });

  it("creates a version 4 UUID from secure random bytes when randomUUID is unavailable", () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => {
      bytes.fill(0);
      return bytes;
    });
    vi.stubGlobal("crypto", { getRandomValues });

    expect(secureUuid()).toBe("00000000-0000-4000-8000-000000000000");
    expect(getRandomValues).toHaveBeenCalledOnce();
  });

  it("fails instead of using a weak random source when browser crypto is unavailable", () => {
    vi.stubGlobal("crypto", undefined);

    expect(() => secureUuid()).toThrow("Secure UUID generation is unavailable");
  });
});
