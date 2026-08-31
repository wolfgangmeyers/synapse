import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { Sidebar } from "../../src/components/Sidebar";

describe("Sidebar", () => {
  it("filters and selects agents", () => {
    const onSelect = vi.fn();
    render(<Sidebar routerLabel="local" connection="connected" agents={[{ agent_id: "alpha.bot" }, { agent_id: "beta" }]} onSelectAgent={onSelect} />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "alpha" } });
    expect(screen.getByRole("button", { name: /Alpha Bot/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Beta/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Alpha Bot/i }));
    expect(onSelect).toHaveBeenCalledWith("alpha.bot");
  });
});
