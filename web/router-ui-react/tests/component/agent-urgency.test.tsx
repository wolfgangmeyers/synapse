import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AgentRow } from "../../src/components/AgentRow";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import type { Agent } from "../../src/state/agent-projection";
import { readFileSync } from "node:fs";

afterEach(cleanup);
const base: Agent = { agent_id: "worker", source: "router-managed", urgent: false };
const obsolete = '.urgency-control, .urgency-toggle, .urgency-badge, .urgency-rail, .urgency-error, .is-urgent';

describe("tool-driven urgency presentation (URG-001/003/004)", () => {
  it.each([false, undefined])("renders no urgency element or reserved slot for %s, including hover/focus", (urgent) => {
    const agent = urgent === undefined ? { agent_id: "worker" } : { ...base, urgent };
    const { container } = render(<AgentRow agent={agent} />);
    fireEvent.mouseOver(screen.getByRole("listitem"));
    fireEvent.focus(screen.getByRole("button"));
    expect(container.querySelector('[class*="urgency"], [aria-label*="Urgent"], [aria-label*="urgent"]')).toBeNull();
    expect(container.querySelector(obsolete)).toBeNull();
  });

  it("renders exactly one passive accessible triangle only for authoritative true", () => {
    const select = vi.fn();
    const { container, rerender } = render(<AgentRow agent={{ ...base, urgent: true }} onSelect={select} />);
    const marker = screen.getByRole("img", { name: "Urgent" });
    expect(marker.tagName.toLowerCase()).toBe("i");
    expect(container.querySelectorAll("i.agent-urgency-marker")).toHaveLength(1);
    expect(marker.classList.contains("fa-solid")).toBe(true);
    expect(marker.classList.contains("fa-triangle-exclamation")).toBe(true);
    expect(marker.textContent).toBe("");
    expect(container.querySelector("svg.agent-urgency-marker")).toBeNull();
    expect(marker.classList.contains("agent-urgency-marker")).toBe(true);
    expect(marker.hasAttribute("tabindex")).toBe(false);
    expect(marker.hasAttribute("aria-pressed")).toBe(false);
    expect(marker.closest("button")).toBeNull();
    expect(container.querySelector(obsolete)).toBeNull();
    fireEvent.click(marker);
    expect(select).toHaveBeenCalledWith("worker");
    rerender(<AgentRow agent={{ ...base, urgent: false }} />);
    expect(screen.queryByRole("img", { name: "Urgent" })).toBeNull();
    expect(container.querySelector('[class*="urgency"]')).toBeNull();
  });

  it("ships exact-version local Font Awesome CSS, webfonts and license without the CDN", () => {
    const manifest = JSON.parse(readFileSync("package.json", "utf8"));
    expect(manifest.dependencies["@fortawesome/fontawesome-free"]).toBe("6.7.2");
    expect(readFileSync("src/main.tsx", "utf8")).toContain('import "@fortawesome/fontawesome-free/css/all.min.css"');
    expect(readFileSync("index.html", "utf8")).not.toMatch(/cdnjs|font-awesome.*https/);
    const css = readFileSync("node_modules/@fortawesome/fontawesome-free/css/all.min.css", "utf8");
    expect(css).toContain("../webfonts/fa-solid-900.woff2");
    expect(css).toContain("../webfonts/fa-regular-400.woff2");
    expect(css).toContain("../webfonts/fa-brands-400.woff2");
    expect(readFileSync("public/licenses/fontawesome-free-6.7.2.txt", "utf8")).toBe(
      readFileSync("node_modules/@fortawesome/fontawesome-free/LICENSE.txt", "utf8"),
    );
  });

  it("keeps selection, blocked, workflow bolt, compacting and progress intact", () => {
    const { container } = render(<AgentRow selected agent={{ ...base, urgent: true, blocked: true, blocked_reason: "waiting", workflow_state: "review", workflow_name: "Ship", activity_state: "compacting", progress: { percent: 42, note: "checking", updated_at: "2030-01-01T00:00:00Z" } }} />);
    expect(screen.getByRole("listitem").classList.contains("active")).toBe(true);
    expect(screen.getByRole("listitem").classList.contains("compacting")).toBe(true);
    expect(screen.getByRole("img", { name: "Urgent" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Blocked: waiting" })).toBeTruthy();
    expect(container.querySelectorAll('.agent-row-workflow-badge .fa-bolt')).toHaveLength(1);
    expect(screen.getByText("Compacting")).toBeTruthy();
    expect(screen.getByText("42%")).toBeTruthy();
    expect(container.querySelector(obsolete)).toBeNull();
  });

  it.each([false, true])("never adds header urgency UI when urgent=%s", (urgent) => {
    const { container } = render(<ConversationHeader agent={{ ...base, urgent }} />);
    expect(container.querySelector('[class*="urgency"], .is-urgent')).toBeNull();
    expect(screen.queryByLabelText(/urgen/i)).toBeNull();
    expect(screen.queryByText(/saving urgency|retry/i)).toBeNull();
  });
});
