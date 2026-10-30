import { describe, expect, it } from "vitest";
import { attachmentReferencesFromBody, pendingAttachmentText, resolveAttachmentUrl, stripAttachmentFooter, validateImageBatch } from "../../src/domain/images";
describe("images", () => {
  it("enforces image bounds", () => { expect(validateImageBatch([{ dataUrl: "x", name: "a", sizeBytes: 768 * 1024, mimeType: "image/jpeg" }, { dataUrl: "x", name: "b", sizeBytes: 1, mimeType: "image/jpeg" }, { dataUrl: "x", name: "c", sizeBytes: 1, mimeType: "image/jpeg" }, { dataUrl: "x", name: "d", sizeBytes: 1, mimeType: "image/jpeg" }])).toContain("three"); });
  it("preserves visible names and resolves only attachment references", () => {
    expect(pendingAttachmentText([{ name: "cat.png" }])).toContain("cat.png");
    expect(resolveAttachmentUrl("http://r", "/v1/attachments/a.jpg")).toBe("http://r/v1/attachments/a.jpg");
    expect(resolveAttachmentUrl("http://r", "https://evil")).toBe("");
    expect(attachmentReferencesFromBody("/v1/attachments/a.jpg", "http://r")).toHaveLength(1);
    const diskPathRefs = attachmentReferencesFromBody("Saved screenshot to /home/user/.mecha/router.sqlite.attachments/foo.png and router.sqlite.attachments/bar.jpg", "http://127.0.0.1:8765");
    expect(diskPathRefs).toHaveLength(2);
    expect(diskPathRefs[0]?.url).toBe("http://127.0.0.1:8765/v1/attachments/foo.png");
    expect(diskPathRefs[0]?.id).toBe("foo.png");
    expect(diskPathRefs[1]?.url).toBe("http://127.0.0.1:8765/v1/attachments/bar.jpg");
    expect(diskPathRefs[1]?.id).toBe("bar.jpg");
  });
  it("strips trailing attachment footer from message body", () => {
    expect(stripAttachmentFooter("testing image attachments\n\nAttached image:\n1. /home/user/.mecha/router.sqlite.attachments/d33bfd3f.png")).toBe("testing image attachments");
    expect(stripAttachmentFooter("testing image attachments\n\nAttached images:\n1. /home/user/a.png\n2. /home/user/b.png")).toBe("testing image attachments");
    expect(stripAttachmentFooter("Attached image:\n1. /tmp/pic.png")).toBe("");
    expect(stripAttachmentFooter("testing image attachments")).toBe("testing image attachments");
    expect(stripAttachmentFooter(undefined)).toBe("");
  });
});
