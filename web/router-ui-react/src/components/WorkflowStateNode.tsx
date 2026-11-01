import { memo } from "react";
import { Handle, Position, type NodeProps } from "@xyflow/react";
import type { WorkflowState, Workflow } from "../domain/workflow-states";

export interface WorkflowStateNodeData {
  state: WorkflowState;
  workflow: Workflow;
  isEntry: boolean;
  onOpenConfig: (state: WorkflowState) => void;
  onDeleteState: (state: WorkflowState) => void;
}

function WorkflowStateNodeComponent({ data, selected }: NodeProps) {
  const nodeData = data as unknown as WorkflowStateNodeData;
  const { state, isEntry, onOpenConfig, onDeleteState } = nodeData;

  const hasRestrictedTools = Boolean(state.allowed_tools && state.allowed_tools.length > 0);
  const toolCount = state.allowed_tools?.length ?? 0;
  const hasReminders = Boolean(state.reminders && state.reminders.length > 0);
  const reminderCount = state.reminders?.length ?? 0;
  const canExit = state.allow_exit !== false;

  return (
    <div className={`workflow-node-card ${selected ? "selected" : ""} ${isEntry ? "is-entry" : ""}`}>
      {/* Target handle on left */}
      <Handle
        type="target"
        position={Position.Left}
        className="workflow-node-handle target-handle"
        id="target"
      />

      <div className="workflow-node-header">
        <div className="workflow-node-title-wrap">
          <div className="workflow-node-name" title={state.name}>
            {state.name}
          </div>
          {state.description ? (
            <div className="workflow-node-desc" title={state.description}>
              {state.description}
            </div>
          ) : null}
        </div>

        <div className="workflow-node-actions nodrag">
          <button
            type="button"
            className="node-action-btn"
            title="Configure State"
            aria-label={`Configure ${state.name}`}
            onClick={(e) => {
              e.stopPropagation();
              onOpenConfig(state);
            }}
          >
            <i className="fa-solid fa-gear" aria-hidden="true" />
          </button>
          <button
            type="button"
            className="node-action-btn danger"
            title="Delete State"
            aria-label={`Delete ${state.name}`}
            onClick={(e) => {
              e.stopPropagation();
              onDeleteState(state);
            }}
          >
            <i className="fa-solid fa-trash" aria-hidden="true" />
          </button>
        </div>
      </div>

      <div className="workflow-node-badges">
        {isEntry && (
          <span className="node-badge badge-entry" title="Workflow entry state">
            <i className="fa-solid fa-play" aria-hidden="true" /> ENTRY
          </span>
        )}

        {hasRestrictedTools ? (
          <span className="node-badge badge-tool-restricted" title={`Restricted tools: ${state.allowed_tools?.join(", ")}`}>
            <i className="fa-solid fa-wrench" aria-hidden="true" /> Restricted ({toolCount})
          </span>
        ) : (
          <span className="node-badge badge-tool-all" title="All tools allowed">
            <i className="fa-solid fa-wrench" aria-hidden="true" /> All Tools
          </span>
        )}

        {hasReminders && (
          <span className="node-badge badge-reminders" title={`${reminderCount} scheduled reminder(s)`}>
            <i className="fa-solid fa-clock" aria-hidden="true" /> {reminderCount} {reminderCount === 1 ? "Reminder" : "Reminders"}
          </span>
        )}

        {canExit ? (
          <span className="node-badge badge-exit-allowed" title="Agent can exit or complete workflow from this state">
            <i className="fa-solid fa-door-open" aria-hidden="true" /> Can Exit
          </span>
        ) : (
          <span className="node-badge badge-exit-locked" title="Agent cannot exit from this state">
            <i className="fa-solid fa-lock" aria-hidden="true" /> No Exit
          </span>
        )}
      </div>

      {state.entry_text ? (
        <div className="workflow-node-entry-preview" title={state.entry_text}>
          <span className="preview-label">Prompt:</span> {state.entry_text}
        </div>
      ) : null}

      {/* Source handle on right */}
      <Handle
        type="source"
        position={Position.Right}
        className="workflow-node-handle source-handle"
        id="source"
      />
    </div>
  );
}

export const WorkflowStateNode = memo(WorkflowStateNodeComponent);
