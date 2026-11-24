import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AttachmentGallery } from "../../src/components/AttachmentGallery";

const sampleDataUrl =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";

afterEach(() => {
  cleanup();
  document.body.style.overflow = "";
});

describe("AttachmentGallery", () => {
  it("renders data URL directly", () => {
    render(<AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />);
    const img = screen.getByRole("img", { name: "test.png" });
    expect(img).toBeDefined();
    expect(img.getAttribute("src")).toBe(sampleDataUrl);
  });

  it("resolves /v1/attachments/ with routerUrl", () => {
    render(
      <AttachmentGallery
        attachments={[{ id: "1", name: "test.png", url: "/v1/attachments/test.png" }]}
        routerUrl="http://127.0.0.1:8765"
      />,
    );
    const img = screen.getByRole("img", { name: "test.png" });
    expect(img).toBeDefined();
    expect(img.getAttribute("src")).toBe("http://127.0.0.1:8765/v1/attachments/test.png");
  });

  it("opens modal via thumbnail click with portal into document.body", () => {
    const { container } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );
    const button = container.querySelector(".attachment-thumb-btn");
    expect(button).not.toBeNull();
    fireEvent.click(button!);

    const dialog = screen.getByRole("dialog", { name: "Image preview" });
    expect(dialog).toBeDefined();
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(dialog.parentElement).toBe(document.body);
    expect(document.body.querySelector(".image-lightbox")).toBe(dialog);
  });

  it("dismisses modal via close button", () => {
    const { container } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );
    const button = container.querySelector(".attachment-thumb-btn");
    fireEvent.click(button!);

    const closeBtn = screen.getByRole("button", { name: "Close image preview" });
    expect(closeBtn.classList.contains("icon-btn-round")).toBe(true);
    expect(closeBtn.classList.contains("image-lightbox-close")).toBe(true);

    fireEvent.click(closeBtn);
    expect(screen.queryByRole("dialog", { name: "Image preview" })).toBeNull();
    expect(document.body.querySelector(".image-lightbox")).toBeNull();
  });

  it("dismisses modal via Escape key", () => {
    const { container } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );
    const button = container.querySelector(".attachment-thumb-btn");
    fireEvent.click(button!);

    expect(screen.getByRole("dialog", { name: "Image preview" })).toBeDefined();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Image preview" })).toBeNull();
  });

  it("dismisses modal via backdrop click", () => {
    const { container } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );
    const button = container.querySelector(".attachment-thumb-btn");
    fireEvent.click(button!);

    const lightbox = document.body.querySelector(".image-lightbox");
    expect(lightbox).not.toBeNull();
    fireEvent.click(lightbox!);
    expect(document.body.querySelector(".image-lightbox")).toBeNull();
  });

  it("clicking image content does not dismiss modal", () => {
    const { container } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );
    const button = container.querySelector(".attachment-thumb-btn");
    fireEvent.click(button!);

    const previewImg = screen.getByRole("img", { name: "Full-size attachment" });
    expect(previewImg).toBeDefined();
    fireEvent.click(previewImg);

    expect(screen.getByRole("dialog", { name: "Image preview" })).toBeDefined();
    expect(document.body.querySelector(".image-lightbox")).not.toBeNull();
  });

  it("locks body overflow while open and restores on close", () => {
    document.body.style.overflow = "auto";
    const { container } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );
    expect(document.body.style.overflow).toBe("auto");

    const button = container.querySelector(".attachment-thumb-btn");
    fireEvent.click(button!);
    expect(document.body.style.overflow).toBe("hidden");

    const closeBtn = screen.getByRole("button", { name: "Close image preview" });
    fireEvent.click(closeBtn);
    expect(document.body.style.overflow).toBe("auto");
  });

  it("restores body overflow when unmounted while modal is open", () => {
    document.body.style.overflow = "visible";
    const { container, unmount } = render(
      <AttachmentGallery attachments={[{ id: "1", name: "test.png", url: sampleDataUrl }]} />,
    );

    const button = container.querySelector(".attachment-thumb-btn");
    fireEvent.click(button!);
    expect(document.body.style.overflow).toBe("hidden");

    unmount();
    expect(document.body.style.overflow).toBe("visible");
  });
});
