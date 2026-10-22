import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { HeaderActionButton } from "../../src/components/HeaderActionButton";

describe("HeaderActionButton", () => {
  afterEach(() => {
    cleanup();
  });

  it("renders with default classes and icon", () => {
    render(<HeaderActionButton icon="fa-solid fa-flag" aria-label="Flag" />);
    const btn = screen.getByRole("button", { name: "Flag" });
    expect(btn).toHaveClass("icon-btn-round", "header-action-button");
    expect(btn.querySelector("i")).toHaveClass("fa-solid", "fa-flag");
  });

  it("applies active class and tone class when active", () => {
    render(
      <HeaderActionButton
        icon="fa-solid fa-flag"
        active
        activeTone="pink"
        aria-label="Flag"
      />
    );
    const btn = screen.getByRole("button", { name: "Flag" });
    expect(btn).toHaveClass("active", "header-action-button--active-pink");
  });

  it("handles click events", () => {
    const handleClick = vi.fn();
    render(<HeaderActionButton aria-label="Test" onClick={handleClick} />);
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(handleClick).toHaveBeenCalledTimes(1);
  });

  it("renders optional badge", () => {
    render(
      <HeaderActionButton aria-label="Test" badge="3" />
    );
    expect(screen.getByText("3")).toHaveClass("header-action-button-badge");
  });
});
