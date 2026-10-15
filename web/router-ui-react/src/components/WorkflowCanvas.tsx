import { useCallback, useEffect, useMemo, useRef, useState, type ReactElement } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  applyNodeChanges,
  applyEdgeChanges,
  type Node,
  type Edge,
  type OnNodesChange,
  type OnEdgesChange,
  type OnConnect,
  type Connection,
  BackgroundVariant,
  MarkerType,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import dagre from "dagre";
import type { RouterHttpClient } from "../transport/http-client";
import type {
  Workflow,
  WorkflowState,
  WorkflowWithStates,
  WorkflowStatePosition,
  WorkflowTransitionConfig,
  KnownToolDefinition,
} from "../domain/workflow-states";
import { WorkflowStateNode, type WorkflowStateNodeData } from "./WorkflowStateNode";
import { StateConfigModal } from "./StateConfigModal";
import { EdgeConfigModal } from "./EdgeConfigModal";
import { WorkflowVariablesModal } from "./WorkflowVariablesModal";
import { DialogShell } from "./DialogShell";

export interface WorkflowCanvasProps {
  workflowId: string;
  client: RouterHttpClient;
  onBack: () => void;
}

const NODE_WIDTH = 260;
const NODE_HEIGHT = 160;

function computeDagreLayout(nodes: Node[], edges: Edge[]): Node[] {
  const dagreGraph = new dagre.graphlib.Graph();
  dagreGraph.setDefaultEdgeLabel(() => ({}));
  dagreGraph.setGraph({ rankdir: "LR", ranksep: 80, nodesep: 50 });

  nodes.forEach((node) => {
    dagreGraph.setNode(node.id, { width: NODE_WIDTH, height: NODE_HEIGHT });
  });

  edges.forEach((edge) => {
    dagreGraph.setEdge(edge.source, edge.target);
  });

  dagre.layout(dagreGraph);

  return nodes.map((node) => {
    const nodeWithPos = dagreGraph.node(node.id);
    if (!nodeWithPos) return node;
    // Dagre returns center coordinates, React Flow uses top-left
    return {
      ...node,
      position: {
        x: nodeWithPos.x - NODE_WIDTH / 2,
        y: nodeWithPos.y - NODE_HEIGHT / 2,
      },
    };
  });
}

interface HealthIssue {
  type: "warning" | "danger";
  message: string;
}

export function WorkflowCanvas({ workflowId, client, onBack }: WorkflowCanvasProps): ReactElement {
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [states, setStates] = useState<WorkflowState[]>([]);
  const [initialLoading, setInitialLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // React Flow state
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);

  // Dialogs
  const [configState, setConfigState] = useState<WorkflowState | null>(null);
  const [deleteConfirmState, setDeleteConfirmState] = useState<WorkflowState | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<Edge | null>(null);
  const [variablesModalOpen, setVariablesModalOpen] = useState(false);
  const [workflowTools, setWorkflowTools] = useState<KnownToolDefinition[] | undefined>(undefined);

  // Fetch tools from backend
  useEffect(() => {
    if (client && typeof client.workflowTools === "function") {
      client.workflowTools()
        .then((res) => {
          if (res?.tools && Array.isArray(res.tools)) {
            setWorkflowTools(res.tools);
          }
        })
        .catch((err) => {
          console.warn("Failed to fetch workflow tools, falling back to defaults", err);
        });
    }
  }, [client]);

  // Add state dialog
  const [addStateDialogOpen, setAddStateDialogOpen] = useState(false);
  const [newStateName, setNewStateName] = useState("");
  const [newStateDesc, setNewStateDesc] = useState("");
  const [addingState, setAddingState] = useState(false);
  const [addStateError, setAddStateError] = useState<string | null>(null);

  // Status message
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  // Unsaved layout changes tracking
  const [hasUnsavedLayout, setHasUnsavedLayout] = useState(false);
  const [savingLayout, setSavingLayout] = useState(false);

  // Initial fitView control so subsequent state additions or updates don't reset viewport zoom
  const initialFitViewDone = useRef(false);

  const nodeTypes = useMemo(() => ({ workflowState: WorkflowStateNode }), []);

  // Fetch workflow and states
  const loadWorkflowData = useCallback(async () => {
    try {
      setError(null);
      const res: WorkflowWithStates = await client.getWorkflow(workflowId);
      setWorkflow(res.workflow);
      setStates(res.states);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to load workflow");
    } finally {
      setInitialLoading(false);
    }
  }, [client, workflowId]);

  useEffect(() => {
    void loadWorkflowData();
  }, [loadWorkflowData]);

  const handleOpenConfig = useCallback((st: WorkflowState) => {
    setConfigState(st);
  }, []);

  const handleDeleteStatePrompt = useCallback((st: WorkflowState) => {
    setDeleteConfirmState(st);
  }, []);

  // Convert states to React Flow nodes and edges
  useEffect(() => {
    if (!workflow) return;

    setNodes((prevNodes) => {
      const prevNodeMap = new Map(prevNodes.map((n) => [n.id, n]));
      return states.map((st, index) => {
        const prev = prevNodeMap.get(st.id);
        const x = prev?.position.x ?? (st.ui_pos_x !== undefined && st.ui_pos_x !== 0 ? st.ui_pos_x : 50 + (index % 3) * (NODE_WIDTH + 80));
        const y = prev?.position.y ?? (st.ui_pos_y !== undefined && st.ui_pos_y !== 0 ? st.ui_pos_y : 50 + Math.floor(index / 3) * (NODE_HEIGHT + 60));

        const nodeData: WorkflowStateNodeData = {
          state: st,
          workflow,
          isEntry: workflow.entry_state_id === st.id,
          onOpenConfig: handleOpenConfig,
          onDeleteState: handleDeleteStatePrompt,
        };

        return {
          id: st.id,
          type: "workflowState",
          position: { x, y },
          data: nodeData as unknown as Record<string, unknown>,
        };
      });
    });

    const newEdges: Edge[] = [];
    states.forEach((st) => {
      if (st.transitions) {
        st.transitions.forEach((tr, trIdx) => {
          newEdges.push({
            id: `edge-${st.id}-${tr.target_state_id}-${trIdx}`,
            source: st.id,
            target: tr.target_state_id,
            label: undefined,
            type: "bezier",
            interactionWidth: 20,
            markerEnd: {
              type: MarkerType.ArrowClosed,
              width: 16,
              height: 16,
              color: "#38bdf8",
            },
            style: { stroke: "#38bdf8", strokeWidth: 2 },
            data: { description: tr.description },
          });
        });
      }
    });

    setEdges(newEdges);
  }, [workflow, states, handleOpenConfig, handleDeleteStatePrompt]);

  // Compute graph health checks
  const healthIssues: HealthIssue[] = useMemo(() => {
    if (!workflow || states.length === 0) return [];
    const issues: HealthIssue[] = [];

    // 1. Entry state designated?
    if (!workflow.entry_state_id || !states.some((s) => s.id === workflow.entry_state_id)) {
      issues.push({
        type: "danger",
        message: "No entry state is designated for this workflow. Configure a state and mark it as entry.",
      });
    }

    // 2. Reachability from entry state
    if (workflow.entry_state_id && states.some((s) => s.id === workflow.entry_state_id)) {
      const reachable = new Set<string>();
      const queue = [workflow.entry_state_id];
      reachable.add(workflow.entry_state_id);

      while (queue.length > 0) {
        const currId = queue.shift()!;
        const currState = states.find((s) => s.id === currId);
        if (currState?.transitions) {
          for (const tr of currState.transitions) {
            if (!reachable.has(tr.target_state_id)) {
              reachable.add(tr.target_state_id);
              queue.push(tr.target_state_id);
            }
          }
        }
      }

      const unreachable = states.filter((s) => !reachable.has(s.id));
      if (unreachable.length > 0) {
        issues.push({
          type: "warning",
          message: `Unreachable states from entry: ${unreachable.map((s) => s.name).join(", ")}.`,
        });
      }
    }

    // 3. Dead-end states (no outgoing transitions and allow_exit === false)
    const deadEnds = states.filter((s) => {
      const hasOutgoing = s.transitions && s.transitions.length > 0;
      const canExit = s.allow_exit !== false;
      return !hasOutgoing && !canExit;
    });

    if (deadEnds.length > 0) {
      issues.push({
        type: "danger",
        message: `Dead-end state(s) without exit permission: ${deadEnds.map((s) => s.name).join(", ")}. Agents will get trapped here.`,
      });
    }

    return issues;
  }, [workflow, states]);

  const onNodesChange: OnNodesChange = useCallback((changes) => {
    setNodes((nds) => applyNodeChanges(changes, nds));
  }, []);

  const onEdgesChange: OnEdgesChange = useCallback((changes) => {
    setEdges((eds) => applyEdgeChanges(changes, eds));
  }, []);

  // Persist node positions on drag stop
  const onNodeDragStop = useCallback(
    (_event: unknown, _node: Node, _allNodes: Node[]) => {
      setHasUnsavedLayout(true);
    },
    []
  );

  // Manual save layout
  const handleSaveLayout = useCallback(async () => {
    setSavingLayout(true);
    const positions: WorkflowStatePosition[] = nodes.map((n) => ({
      state_id: n.id,
      ui_pos_x: Math.round(n.position.x),
      ui_pos_y: Math.round(n.position.y),
    }));

    try {
      await client.updateWorkflowLayout(workflowId, positions);
      setHasUnsavedLayout(false);
      setStatusMessage("Layout saved");
      setTimeout(() => setStatusMessage(null), 2000);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to save layout");
    } finally {
      setSavingLayout(false);
    }
  }, [client, workflowId, nodes]);

  // Tidy Layout button
  const handleTidyLayout = async () => {
    const layouted = computeDagreLayout(nodes, edges);
    setNodes([...layouted]);

    const positions: WorkflowStatePosition[] = layouted.map((n) => ({
      state_id: n.id,
      ui_pos_x: Math.round(n.position.x),
      ui_pos_y: Math.round(n.position.y),
    }));

    try {
      await client.updateWorkflowLayout(workflowId, positions);
      setHasUnsavedLayout(false);
      setStatusMessage("Layout reorganized & saved");
      setTimeout(() => setStatusMessage(null), 2500);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to persist tidy layout");
    }
  };

  // Add Transition by connecting handles
  const onConnect: OnConnect = useCallback(
    async (connection: Connection) => {
      if (!connection.source || !connection.target) return;
      if (connection.source === connection.target) {
        setError("Self-loops are not permitted in workflow transitions.");
        setTimeout(() => setError(null), 4000);
        return;
      }

      const sourceState = states.find((s) => s.id === connection.source);
      if (!sourceState) return;

      const existingTransitions = sourceState.transitions ?? [];
      if (existingTransitions.some((t) => t.target_state_id === connection.target)) {
        setError("Transition already exists between these states.");
        setTimeout(() => setError(null), 4000);
        return;
      }

      const newTransitions: WorkflowTransitionConfig[] = [
        ...existingTransitions,
        { target_state_id: connection.target, description: "" },
      ];

      try {
        await client.updateWorkflowState(sourceState.id, {
          name: sourceState.name,
          description: sourceState.description,
          entry_text: sourceState.entry_text,
          goal: sourceState.goal,
          reminders: sourceState.reminders,
          allowed_tools: sourceState.allowed_tools,
          allow_exit: sourceState.allow_exit,
          transitions: newTransitions,
          workflow_id: sourceState.workflow_id,
          ui_pos_x: sourceState.ui_pos_x,
          ui_pos_y: sourceState.ui_pos_y,
        });
        await loadWorkflowData();
        setStatusMessage("Transition added");
        setTimeout(() => setStatusMessage(null), 2000);
      } catch (err: unknown) {
        setError(err instanceof Error ? err.message : "Failed to add transition");
      }
    },
    [states, client, loadWorkflowData]
  );

  // Edge click handler
  const onEdgeClick = (_event: React.MouseEvent, edge: Edge) => {
    setSelectedEdge(edge);
  };

  // Save transition description
  const handleSaveEdge = async (newDesc: string) => {
    if (!selectedEdge) return;
    const sourceState = states.find((s) => s.id === selectedEdge.source);
    if (!sourceState) return;

    const updatedTransitions = (sourceState.transitions ?? []).map((tr) => {
      if (tr.target_state_id === selectedEdge.target) {
        return { ...tr, description: newDesc };
      }
      return tr;
    });

    try {
      await client.updateWorkflowState(sourceState.id, {
        name: sourceState.name,
        description: sourceState.description,
        entry_text: sourceState.entry_text,
        goal: sourceState.goal,
        reminders: sourceState.reminders,
        allowed_tools: sourceState.allowed_tools,
        allow_exit: sourceState.allow_exit,
        transitions: updatedTransitions,
      });
      await loadWorkflowData();
      setStatusMessage("Transition updated");
      setTimeout(() => setStatusMessage(null), 2000);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to update transition");
    }
  };

  // Delete transition
  const handleDeleteEdge = async () => {
    if (!selectedEdge) return;
    const sourceState = states.find((s) => s.id === selectedEdge.source);
    if (!sourceState) return;

    const updatedTransitions = (sourceState.transitions ?? []).filter(
      (tr) => tr.target_state_id !== selectedEdge.target
    );

    try {
      await client.updateWorkflowState(sourceState.id, {
        name: sourceState.name,
        description: sourceState.description,
        entry_text: sourceState.entry_text,
        goal: sourceState.goal,
        reminders: sourceState.reminders,
        allowed_tools: sourceState.allowed_tools,
        allow_exit: sourceState.allow_exit,
        transitions: updatedTransitions,
      });
      await loadWorkflowData();
      setStatusMessage("Transition removed");
      setTimeout(() => setStatusMessage(null), 2000);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete transition");
    }
  };

  // Add new state to workflow
  const handleAddState = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!newStateName.trim()) {
      setAddStateError("State name is required");
      return;
    }

    setAddingState(true);
    setAddStateError(null);
    try {
      // Drop in middle of screen or offset based on state count
      const defaultX = 100 + (states.length % 4) * (NODE_WIDTH + 60);
      const defaultY = 100 + Math.floor(states.length / 4) * (NODE_HEIGHT + 60);

      const res = await client.createWorkflowState({
        workflow_id: workflowId,
        name: newStateName.trim(),
        description: newStateDesc.trim(),
        entry_text: "",
        ui_pos_x: defaultX,
        ui_pos_y: defaultY,
      });

      // If workflow has no entry state yet, automatically set this as entry
      if (!workflow?.entry_state_id) {
        const updatePayload: { name?: string; description?: string; entry_state_id?: string | null } = {
          entry_state_id: res.workflow_state.id,
        };
        if (workflow?.name !== undefined) {
          updatePayload.name = workflow.name;
        }
        if (workflow?.description !== undefined) {
          updatePayload.description = workflow.description;
        }
        await client.updateWorkflow(workflowId, updatePayload);
      }

      setAddStateDialogOpen(false);
      setNewStateName("");
      setNewStateDesc("");
      await loadWorkflowData();
      setStatusMessage("State created");
      setTimeout(() => setStatusMessage(null), 2000);
    } catch (err: unknown) {
      setAddStateError(err instanceof Error ? err.message : "Failed to create state");
    } finally {
      setAddingState(false);
    }
  };

  // Save state config modal
  const handleSaveStateConfig = async (updatedState: WorkflowState, makeEntry: boolean) => {
    await client.updateWorkflowState(updatedState.id, {
      name: updatedState.name,
      description: updatedState.description,
      entry_text: updatedState.entry_text,
      goal: updatedState.goal,
      reminders: updatedState.reminders,
      allowed_tools: updatedState.allowed_tools,
      allow_exit: updatedState.allow_exit,
      transitions: updatedState.transitions,
    });

    if (workflow) {
      if (makeEntry && workflow.entry_state_id !== updatedState.id) {
        await client.updateWorkflow(workflow.id, { entry_state_id: updatedState.id });
      } else if (!makeEntry && workflow.entry_state_id === updatedState.id) {
        await client.updateWorkflow(workflow.id, { entry_state_id: null });
      }
    }

    await loadWorkflowData();
    setStatusMessage("State settings saved");
    setTimeout(() => setStatusMessage(null), 2000);
  };

  // Delete state
  const handleDeleteState = async () => {
    if (!deleteConfirmState) return;
    try {
      await client.deleteWorkflowState(deleteConfirmState.id);
      setDeleteConfirmState(null);
      await loadWorkflowData();
      setStatusMessage("State deleted");
      setTimeout(() => setStatusMessage(null), 2000);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to delete state");
    }
  };

  const selectedEdgeSource = selectedEdge ? states.find((s) => s.id === selectedEdge.source) : null;
  const selectedEdgeTarget = selectedEdge ? states.find((s) => s.id === selectedEdge.target) : null;

  return (
    <div className="workflows-page workflow-canvas-wrapper" style={{ height: "100%", display: "flex", flexDirection: "column" }}>
      {/* Canvas Top Bar */}
      <header className="workflows-header canvas-header">
        <div className="workflows-nav">
          <button type="button" className="workflows-back-btn" onClick={onBack}>
            <i className="fa-solid fa-arrow-left" aria-hidden="true" /> Workflows
          </button>
          <div className="workflows-title">
            <h2>{workflow?.name ?? "Workflow Canvas"}</h2>
            {workflow?.description ? <span className="canvas-header-desc">{workflow.description}</span> : null}
          </div>
        </div>

        <div className="canvas-toolbar-actions">
          {hasUnsavedLayout && (
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => void handleSaveLayout()}
              disabled={savingLayout}
              title="Save modified node positions"
            >
              <i className="fa-solid fa-floppy-disk" aria-hidden="true" /> {savingLayout ? "Saving..." : "Save Layout"}
            </button>
          )}
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => void handleTidyLayout()}
            title="Auto-arrange nodes left-to-right"
          >
            <i className="fa-solid fa-wand-magic-sparkles" aria-hidden="true" /> Tidy Layout
          </button>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => setVariablesModalOpen(true)}
            title="Configure workflow variables"
          >
            <i className="fa-solid fa-sliders" aria-hidden="true" /> Variables
            {workflow?.variables && workflow.variables.length > 0 ? ` (${workflow.variables.length})` : ""}
          </button>
          <button
            type="button"
            className="btn btn-secondary"
            onClick={async () => {
              if (!workflow) return;
              try {
                const bundle = await client.exportWorkflow(workflow.id);
                const dataStr = "data:text/json;charset=utf-8," + encodeURIComponent(JSON.stringify(bundle, null, 2));
                const downloadAnchor = document.createElement("a");
                const sanitizedName = workflow.name.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-|-$/g, "") || "workflow";
                downloadAnchor.setAttribute("href", dataStr);
                downloadAnchor.setAttribute("download", `${sanitizedName}-workflow.json`);
                document.body.appendChild(downloadAnchor);
                downloadAnchor.click();
                downloadAnchor.remove();
                setStatusMessage("Workflow exported");
                setTimeout(() => setStatusMessage(null), 2000);
              } catch (err: unknown) {
                setError(err instanceof Error ? err.message : "Failed to export workflow");
              }
            }}
            title="Export workflow to JSON"
          >
            <i className="fa-solid fa-file-export" aria-hidden="true" /> Export
          </button>
          <button
            type="button"
            className="btn btn-primary"
            onClick={() => {
              setNewStateName("");
              setNewStateDesc("");
              setAddStateError(null);
              setAddStateDialogOpen(true);
            }}
          >
            <i className="fa-solid fa-plus" aria-hidden="true" /> Add State
          </button>
        </div>
      </header>

      {/* Global Error Banner */}
      {error ? (
        <div
          className="alert alert-danger canvas-error-alert"
          role="alert"
          style={{
            color: "#f87171",
            backgroundColor: "rgba(127, 29, 29, 0.4)",
            border: "1px solid #991b1b",
            borderRadius: "6px",
            padding: "8px 12px",
            fontSize: "13px",
            marginBottom: "12px",
          }}
        >
          {error}
        </div>
      ) : null}

      {/* Health Check Banner */}
      {healthIssues.length > 0 && (
        <div className="workflow-health-banner">
          <div className="health-banner-title">
            <i className="fa-solid fa-triangle-exclamation" aria-hidden="true" /> Workflow Health Checks
          </div>
          <ul className="health-issues-list">
            {healthIssues.map((issue, idx) => (
              <li key={idx} className={`health-issue-item ${issue.type}`}>
                {issue.message}
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* React Flow Canvas */}
      <div className="canvas-flow-container" style={{ flex: 1, position: "relative", minHeight: "450px" }}>
        {statusMessage && (
          <div
            className="canvas-status-toast animate-fade"
            style={{
              position: "absolute",
              top: "16px",
              right: "16px",
              zIndex: 10,
              pointerEvents: "none",
            }}
          >
            <span className="canvas-status-pill">
              <i className="fa-solid fa-check" aria-hidden="true" /> {statusMessage}
            </span>
          </div>
        )}
        {initialLoading ? (
          <div className="workflows-loading" style={{ height: "100%", display: "flex", alignItems: "center", justifyContent: "center" }}>
            Loading workflow canvas...
          </div>
        ) : (
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onNodeDragStop={onNodeDragStop}
            onEdgeClick={onEdgeClick}
            fitView={!initialFitViewDone.current}
            onInit={() => {
              initialFitViewDone.current = true;
            }}
            snapToGrid
            snapGrid={[15, 15]}
            defaultEdgeOptions={{
              type: "bezier",
              style: { stroke: "#38bdf8", strokeWidth: 2 },
            }}
          >
            <Background variant={BackgroundVariant.Dots} gap={16} size={1} color="#334155" />
            <Controls />
          </ReactFlow>
        )}
      </div>

      {/* Add State Modal */}
      {addStateDialogOpen && (
        <DialogShell
          open={true}
          title="Add Workflow State"
          titleId="add-state-dialog-title"
          description="Create a new state node in this workflow."
          onClose={() => setAddStateDialogOpen(false)}
          closeLabel="Close add state dialog"
          className="message-dialog"
        >
          <form onSubmit={(e) => void handleAddState(e)}>
            {addStateError && (
              <div
                className="alert alert-danger"
                style={{
                  color: "#f87171",
                  backgroundColor: "rgba(127, 29, 29, 0.4)",
                  border: "1px solid #991b1b",
                  borderRadius: "6px",
                  padding: "8px 12px",
                  fontSize: "13px",
                  marginBottom: "12px",
                }}
              >
                {addStateError}
              </div>
            )}
            <div className="form-group" style={{ marginBottom: "1rem" }}>
              <label htmlFor="canvas-state-name" style={{ display: "block", marginBottom: "0.3rem", fontWeight: 600 }}>
                State Name *
              </label>
              <input
                id="canvas-state-name"
                type="text"
                className="form-control"
                placeholder="e.g. Code Review"
                value={newStateName}
                onChange={(e) => setNewStateName(e.target.value)}
                autoFocus
                required
              />
            </div>
            <div className="form-group" style={{ marginBottom: "1.25rem" }}>
              <label htmlFor="canvas-state-desc" style={{ display: "block", marginBottom: "0.3rem", fontWeight: 600 }}>
                Description (optional)
              </label>
              <input
                id="canvas-state-desc"
                type="text"
                className="form-control"
                placeholder="What happens during this step"
                value={newStateDesc}
                onChange={(e) => setNewStateDesc(e.target.value)}
              />
            </div>
            <div className="dialog-actions workflow-dialog-actions">
              <button type="button" className="btn btn-secondary" onClick={() => setAddStateDialogOpen(false)}>
                Cancel
              </button>
              <button type="submit" className="btn btn-primary" disabled={addingState}>
                {addingState ? "Creating..." : "Create State"}
              </button>
            </div>
          </form>
        </DialogShell>
      )}

      {/* State Config Modal (Gear) */}
      {configState && (
        <StateConfigModal
          state={configState}
          isEntry={workflow?.entry_state_id === configState.id}
          tools={workflowTools}
          onClose={() => setConfigState(null)}
          onSave={handleSaveStateConfig}
        />
      )}

      {/* Delete State Confirmation */}
      {deleteConfirmState && (
        <DialogShell
          open={true}
          title="Delete State"
          titleId="delete-state-dialog-title"
          description={`Are you sure you want to delete state "${deleteConfirmState.name}"? Connected transitions will be removed.`}
          onClose={() => setDeleteConfirmState(null)}
          closeLabel="Close delete state confirmation"
          className="message-dialog delete-confirm-dialog"
        >
          <div className="dialog-actions workflow-dialog-actions">
            <button type="button" className="btn btn-secondary" onClick={() => setDeleteConfirmState(null)}>
              Cancel
            </button>
            <button type="button" className="btn btn-danger" onClick={() => void handleDeleteState()}>
              Delete State
            </button>
          </div>
        </DialogShell>
      )}

      {/* Edge Config Modal */}
      {selectedEdge && (
        <EdgeConfigModal
          sourceName={selectedEdgeSource?.name ?? selectedEdge.source}
          targetName={selectedEdgeTarget?.name ?? selectedEdge.target}
          initialDescription={(selectedEdge.data as { description?: string })?.description ?? ""}
          onClose={() => setSelectedEdge(null)}
          onSave={(desc) => void handleSaveEdge(desc)}
          onDelete={() => void handleDeleteEdge()}
        />
      )}

      {/* Workflow Variables Modal */}
      {workflow && variablesModalOpen && (
        <WorkflowVariablesModal
          isOpen={variablesModalOpen}
          workflow={workflow}
          onClose={() => setVariablesModalOpen(false)}
          onSave={async (variables) => {
            await client.updateWorkflow(workflow.id, { variables });
            setWorkflow((prev) => (prev ? { ...prev, variables } : null));
            setStatusMessage("Variables updated");
            setTimeout(() => setStatusMessage(null), 2000);
          }}
        />
      )}
    </div>
  );
}

export default WorkflowCanvas;
