import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

afterEach(cleanup);
import { Composer } from "../../src/components/Composer";

describe("Composer", () => {
  it("uses the legacy paperclip icon in the audited attachment hit box", () => {
    render(<Composer enabled onSend={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Attach images" }).querySelector("i")).toHaveClass("fa-solid", "fa-paperclip");
  });

  it("reports an equal selection only once", () => {
    const onSelectionChange = vi.fn();
    render(<Composer enabled onSend={vi.fn()} onSelectionChange={onSelectionChange} />);
    const input = screen.getByRole("textbox", { name: /message/i });
    fireEvent.select(input, { target: { selectionStart: 0, selectionEnd: 0 } });
    fireEvent.select(input, { target: { selectionStart: 0, selectionEnd: 0 } });
    expect(onSelectionChange).toHaveBeenCalledTimes(1);
    expect(onSelectionChange).toHaveBeenCalledWith({ start: 0, end: 0 });
  });

  it("sends on Enter and keeps a newline for Shift+Enter", () => {
    const onSend = vi.fn();
    render(<Composer enabled onSend={onSend} />);
    const input = screen.getByRole("textbox", { name: /message/i });
    fireEvent.change(input, { target: { value: "first" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSend).toHaveBeenCalledWith("first", []);
    fireEvent.change(input, { target: { value: "first" } });
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    expect(input instanceof HTMLTextAreaElement).toBe(true);
    expect(input instanceof HTMLTextAreaElement ? input.value : "").toContain("\n");
  });

  it("sends a converted image through the composer and resets the attachment", async () => {
    const onSend = vi.fn();
    const image = { dataUrl: "data:image/jpeg;base64,aGVsbG8=", name: "screen.jpg", sizeBytes: 5, mimeType: "image/jpeg" };
    const user = userEvent.setup();
    render(<Composer enabled onSend={onSend} convert={async () => image} />);
    const file = new File(["image"], "screen.jpg", { type: "image/jpeg" });
    const input = document.querySelector("input[type=file]");
    if (!(input instanceof HTMLInputElement)) throw new Error("image input is missing");
    fireEvent.change(input, { target: { files: [file] } });
    await waitFor(() => expect(screen.getByRole("img", { name: "screen.jpg" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(onSend).toHaveBeenCalledWith("", [image]);
    expect(screen.queryByRole("img", { name: "screen.jpg" })).not.toBeInTheDocument();
  });

  it("ignores paste events without clipboard data", () => {
    render(<Composer enabled onSend={vi.fn()} />);
    expect(() => fireEvent.paste(screen.getByTestId("clipboard-paste-target"), { clipboardData: undefined })).not.toThrow();
  });

  it("keeps handling pasted image files", async () => {
    const image = { dataUrl: "data:image/png;base64,aGVsbG8=", name: "pasted.png", sizeBytes: 5, mimeType: "image/png" };
    render(<Composer enabled onSend={vi.fn()} convert={async () => image} />);
    const file = new File(["image"], "pasted.png", { type: "image/png" });
    fireEvent.paste(screen.getByTestId("clipboard-paste-target"), { clipboardData: { files: [file] } });
    await waitFor(() => expect(screen.getByRole("img", { name: "pasted.png" })).toBeInTheDocument());
  });
});
