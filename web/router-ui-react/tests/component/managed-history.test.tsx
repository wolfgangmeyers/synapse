import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { ManagedHistory } from "../../src/components/ManagedHistory";

describe("ManagedHistory", () => {
  it("keeps quiet and complete data separate and filters the selected view", async () => {
    const user = userEvent.setup();
    render(<ManagedHistory quiet={[{ id: "quiet-1", level: "message", message: "Called Wolfgang" }]} complete={[{ id: "complete-1", level: "error", message: "Restart failed" }]} />);
    expect(screen.getByText("Called Wolfgang")).toBeInTheDocument();
    expect(screen.queryByText("Restart failed")).not.toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: "Complete" }));
    expect(screen.getByRole("tab", { name: "Complete" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Restart failed")).toBeInTheDocument();
    await user.type(screen.getByLabelText("Filter history"), "message");
    expect(screen.getByText("No history matches this filter.")).toBeInTheDocument();
  });
});
