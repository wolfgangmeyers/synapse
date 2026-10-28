import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AttachmentGallery } from "../../src/components/AttachmentGallery";

afterEach(cleanup);

describe("AttachmentGallery", () => {
  it("renders data URL directly", () => {
    const dataUrl = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
    render(<AttachmentGallery attachments={[{ id: "1", name: "test.png", url: dataUrl }]} />);
    const img = screen.getByRole("img", { name: "test.png" });
    expect(img).toBeDefined();
    expect(img.getAttribute("src")).toBe(dataUrl);
  });

  it("resolves /v1/attachments/ with routerUrl", () => {
    render(<AttachmentGallery attachments={[{ id: "1", name: "test.png", url: "/v1/attachments/test.png" }]} routerUrl="http://127.0.0.1:8765" />);
    const img = screen.getByRole("img", { name: "test.png" });
    expect(img).toBeDefined();
    expect(img.getAttribute("src")).toBe("http://127.0.0.1:8765/v1/attachments/test.png");
  });

  it("opens and closes lightbox modal on click", () => {
    const dataUrl = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
    const { container } = render(<AttachmentGallery attachments={[{ id: "1", name: "test.png", url: dataUrl }]} />);
    const button = container.querySelector(".attachment-thumb-btn");
    expect(button).not.toBeNull();
    fireEvent.click(button!);
    expect(container.querySelector(".image-lightbox")).not.toBeNull();
    const closeBtn = container.querySelector(".image-lightbox-close");
    expect(closeBtn).not.toBeNull();
    fireEvent.click(closeBtn!);
    expect(container.querySelector(".image-lightbox")).toBeNull();
  });
});
