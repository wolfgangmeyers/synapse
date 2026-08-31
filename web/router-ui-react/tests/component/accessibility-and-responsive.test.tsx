import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { AppShell } from "../../src/components/AppShell";

describe("accessibility and responsive presentation", () => {
  it("keeps named landmarks and the responsive shell class", () => {
    render(<AppShell sidebar={<div />} conversation={<div />} />);
    const shell = screen.getByTestId("app-shell");
    expect(shell.classList.contains("shell")).toBe(true);
    expect(screen.getByRole("region", { name: "Conversation" })).toBeTruthy();
  });
});
