import { isClaudeCodeAgent } from "../state/agent-projection";
import { useEffect, useRef, type ReactElement } from "react";
import type { Agent } from "../state/agent-projection";

export interface AgentControlItem {
  id: string;
  label: string;
  icon: string;
  action?: (() => void) | undefined;
  danger?: boolean | undefined;
  disabled?: boolean | undefined;
  pressed?: boolean | undefined;
}

export interface AgentControlsDropdownProps {
  open: boolean;
  onClose: () => void;
  agent: Agent;
  pinnedControls: string[];
  onTogglePin: (controlId: string) => void;
  onGoal?: (() => void) | undefined;
  onReminders?: (() => void) | undefined;
  onInlineToolSettings?: (() => void) | undefined;
  onAction?: ((action: "restart" | "reset") => void) | undefined;
  onConfirmTermination?: ((confirm: boolean) => void) | undefined;
  onWorkflow?: (() => void) | undefined;
  onToggleInlineTools?: (() => void) | undefined;
  onScratchpad?: (() => void) | undefined;
  onMonitors?: (() => void) | undefined;
  onWorkerTools?: (() => void) | undefined;
  onToggleUrgency?: (() => void) | undefined;
  urgencyDisabled?: boolean | undefined;
  urgent?: boolean | undefined;
  allowInlineTools?: boolean | undefined;
  hasActiveWorkflow?: boolean | undefined;
  activeReminderCount?: number | undefined;
  activeWatchersCount?: number | undefined;
}

export function AgentControlsDropdown({
  open,
  onClose,
  agent: _agent,
  pinnedControls,
  onTogglePin,
  onGoal,
  onReminders,
  onInlineToolSettings,
  onAction,
  onConfirmTermination,
  onWorkflow,
  onToggleInlineTools,
  onScratchpad,
  onMonitors,
  onWorkerTools,
  onToggleUrgency,
  urgencyDisabled,
  urgent,
  allowInlineTools: _allowInlineTools,
  hasActiveWorkflow: _hasActiveWorkflow,
  activeReminderCount: _activeReminderCount,
  activeWatchersCount: _activeWatchersCount,
}: AgentControlsDropdownProps): ReactElement | null {
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };

    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        onClose();
      }
    };

    document.addEventListener("keydown", handleKeyDown);
    // Use capture phase or next tick to avoid immediate triggering by the button click
    const timer = setTimeout(() => {
      document.addEventListener("click", handleClickOutside);
    }, 0);

    return () => {
      clearTimeout(timer);
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("click", handleClickOutside);
    };
  }, [open, onClose]);

  if (!open) return null;

  const controls: AgentControlItem[] = [
    { id: "urgency", label: "Urgency", icon: "fa-solid fa-triangle-exclamation", action: onToggleUrgency, disabled: urgencyDisabled, pressed: urgent },
    { id: "goal", label: "Set goal", icon: "fa-solid fa-flag", action: onGoal },
    { id: "reminders", label: "Reminders", icon: "fa-solid fa-clock", action: onReminders },
    { id: "toolSettings", label: "Inline tool settings", icon: "fa-solid fa-gear", action: onInlineToolSettings },
    { id: "restart", label: "Restart", icon: "fa-solid fa-rotate-left", action: () => onAction?.("restart") },
    { id: "reset", label: "Reset", icon: "fa-solid fa-rotate", action: () => onAction?.("reset") },
    {
      id: "terminate",
      label: "Terminate",
      icon: "fa-regular fa-trash-can",
      action: () => {
        onConfirmTermination?.(true);
      },
      danger: true,
    },
    { id: "workflow", label: "Workflow", icon: "fa-solid fa-bolt", action: onWorkflow },
    { id: "inlineTools", label: "Inline tools", icon: "fa-solid fa-terminal", action: onToggleInlineTools },
    { id: "scratchpad", label: "Scratchpad", icon: "fa-solid fa-note-sticky", action: onScratchpad },
    { id: "monitors", label: "Monitors", icon: "fa-solid fa-shield-halved", action: onMonitors },
    { id: "workerTools", label: "Worker tools", icon: "fa-solid fa-wrench", action: onWorkerTools },
  ];

  return (
    <div className="agent-controls-dropdown-wrapper" ref={dropdownRef}>
      <div className="agent-controls-dropdown" role="menu" aria-label="Agent controls menu">
        <div className="agent-controls-dropdown-header">
          <span className="agent-controls-dropdown-title">Agent Controls</span>
        </div>
        <div className="agent-controls-dropdown-grid">
          {controls.filter(control => !isClaudeCodeAgent(_agent) || !["workflow", "inlineTools", "toolSettings", "workerTools"].includes(control.id)).map((control) => {
            const isPinned = pinnedControls.includes(control.id);
            return (
              <div
                key={control.id}
                className={`agent-control-tile${isPinned ? " pinned" : ""}${control.danger ? " danger" : ""}`}
              >
                <button
                  type="button"
                  className="agent-control-tile-btn"
                  disabled={control.disabled}
                  aria-pressed={control.pressed}
                  onClick={() => {
                    control.action?.();
                    onClose();
                  }}
                  aria-label={control.label}
                  title={control.label}
                >
                  <i className={`${control.icon} agent-control-tile-icon`} aria-hidden="true" />
                  <span className="agent-control-tile-label">{control.label}</span>
                </button>
                <button
                  type="button"
                  className={`agent-control-pin-btn${isPinned ? " pinned" : ""}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    onTogglePin(control.id);
                  }}
                  aria-label={isPinned ? `Unpin ${control.label}` : `Pin ${control.label}`}
                  title={isPinned ? `Unpin ${control.label}` : `Pin ${control.label}`}
                >
                  <i className="fa-solid fa-thumbtack" aria-hidden="true" />
                </button>
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
