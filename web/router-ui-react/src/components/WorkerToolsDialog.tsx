import { useEffect, useState, useMemo, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient, AgentToolItem, AgentToolsResponse } from "../transport/http-client";

export interface WorkerToolsDialogProps {
  open: boolean;
  agentId: string;
  agentName?: string;
  client: RouterHttpClient;
  onClose: () => void;
}

export function WorkerToolsDialog({ open, agentId, agentName, client, onClose }: WorkerToolsDialogProps): ReactElement {
  const [data, setData] = useState<AgentToolsResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [collapsedCategories, setCollapsedCategories] = useState<Record<string, boolean>>({});
  const [updatingCategory, setUpdatingCategory] = useState<string | null>(null);
  const [updatingTool, setUpdatingTool] = useState<string | null>(null);

  const loadAgentTools = async (): Promise<void> => {
    if (!agentId) return;
    setLoading(true);
    setError(null);
    try {
      const resp = await client.getAgentTools(agentId);
      setData(resp);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load worker tools");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open && agentId) {
      void loadAgentTools();
    } else {
      setError(null);
    }
  }, [open, agentId]);

  const tools = data?.tools ?? [];
  const workflowOverride = data?.workflow_override ?? false;
  const activeWorkflowState = data?.active_workflow_state ?? "";

  const categories = useMemo(() => {
    const map = new Map<string, AgentToolItem[]>();
    for (const t of tools) {
      const cat = t.category || "General";
      if (!map.has(cat)) {
        map.set(cat, []);
      }
      map.get(cat)!.push(t);
    }
    return Array.from(map.entries()).sort(([a], [b]) => a.localeCompare(b));
  }, [tools]);

  const toggleCategoryCollapse = (category: string) => {
    setCollapsedCategories((prev) => ({
      ...prev,
      [category]: !prev[category],
    }));
  };

  const handleCategoryToggleAll = async (category: string, enabled: boolean) => {
    setUpdatingCategory(category);
    setError(null);
    // Optimistic update
    setData((prev) => {
      if (!prev) return prev;
      return {
        ...prev,
        tools: prev.tools.map((t) =>
          t.category === category
            ? { ...t, enabled, effective_enabled: workflowOverride ? t.effective_enabled : enabled }
            : t
        ),
      };
    });
    try {
      await client.updateAgentTools(agentId, { category, enabled });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update category tools");
      void loadAgentTools();
    } finally {
      setUpdatingCategory(null);
    }
  };

  const handleToolToggle = async (toolName: string, enabled: boolean) => {
    setUpdatingTool(toolName);
    setError(null);
    // Optimistic update
    setData((prev) => {
      if (!prev) return prev;
      return {
        ...prev,
        tools: prev.tools.map((t) =>
          t.tool_name === toolName
            ? { ...t, enabled, effective_enabled: workflowOverride ? t.effective_enabled : enabled }
            : t
        ),
      };
    });
    try {
      await client.updateAgentTools(agentId, { tool_name: toolName, enabled });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update worker tool");
      void loadAgentTools();
    } finally {
      setUpdatingTool(null);
    }
  };

  const title = `Manage Tools: ${agentName || agentId}`;

  return (
    <DialogShell
      open={open}
      title={title}
      titleId="worker-tools-title"
      description="Configure tool access permissions for this worker."
      onClose={onClose}
      closeLabel="Close worker tools"
      className="message-dialog worker-tools-dialog"
    >
      <div className="worker-tools-container" style={{ display: "flex", flexDirection: "column", gap: "16px", maxHeight: "70vh", overflowY: "auto" }}>
        {error ? (
          <div className="dialog-error" role="alert" style={{ color: "var(--color-danger, #ff5555)", padding: "8px", background: "rgba(255, 85, 85, 0.1)", borderRadius: "4px" }}>
            {error}
          </div>
        ) : null}

        {workflowOverride ? (
          <div
            className="workflow-override-banner"
            role="alert"
            style={{
              display: "flex",
              alignItems: "center",
              gap: "10px",
              padding: "10px 14px",
              background: "rgba(255, 170, 0, 0.12)",
              border: "1px solid rgba(255, 170, 0, 0.3)",
              borderRadius: "6px",
              color: "var(--color-warning, #ffb86c)",
              fontSize: "0.85rem",
            }}
          >
            <i className="fa-solid fa-triangle-exclamation" aria-hidden="true" style={{ fontSize: "1.1rem" }} />
            <div>
              Workflow state &apos;<strong>{activeWorkflowState}</strong>&apos; is active and currently overrides tool permissions. Individual worker tool toggles will apply when the workflow completes or clears.
            </div>
          </div>
        ) : null}

        {loading && tools.length === 0 ? (
          <div style={{ textAlign: "center", padding: "24px", color: "var(--text-secondary, #aaa)" }}>
            Loading worker tools...
          </div>
        ) : categories.length === 0 ? (
          <div style={{ textAlign: "center", padding: "24px", color: "var(--text-secondary, #aaa)" }}>
            No tools found for this worker.
          </div>
        ) : (
          categories.map(([catName, catTools]) => {
            const grantedCount = catTools.filter((t) => t.enabled).length;
            const isCollapsed = Boolean(collapsedCategories[catName]);
            const isCategoryUpdating = updatingCategory === catName;

            return (
              <div
                key={catName}
                className="tool-category-card"
                style={{
                  border: "1px solid var(--border-color, #333)",
                  borderRadius: "8px",
                  background: "var(--surface-primary, rgba(255, 255, 255, 0.02))",
                  overflow: "visible",
                  flexShrink: 0,
                }}
              >
                <div
                  className="tool-category-header"
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "10px 14px",
                    background: "var(--surface-raised, rgba(255, 255, 255, 0.05))",
                    borderRadius: isCollapsed ? "8px" : "8px 8px 0 0",
                    cursor: "pointer",
                    userSelect: "none",
                  }}
                  onClick={() => toggleCategoryCollapse(catName)}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                    <i
                      className={`fa-solid fa-chevron-${isCollapsed ? "right" : "down"}`}
                      style={{ fontSize: "0.8rem", width: "14px" }}
                      aria-hidden="true"
                    />
                    <strong style={{ fontSize: "0.95rem" }}>{catName}</strong>
                    <span
                      style={{
                        fontSize: "0.75rem",
                        padding: "2px 8px",
                        borderRadius: "12px",
                        background: grantedCount > 0 ? "rgba(76, 175, 80, 0.15)" : "rgba(255, 255, 255, 0.1)",
                        color: grantedCount > 0 ? "var(--color-success, #4caf50)" : "var(--text-secondary, #888)",
                      }}
                    >
                      {grantedCount} / {catTools.length} granted
                    </span>
                  </div>
                  <div
                    style={{ display: "flex", gap: "6px" }}
                    onClick={(e) => e.stopPropagation()}
                  >
                    <button
                      type="button"
                      className="secondary"
                      style={{ fontSize: "0.75rem", padding: "3px 8px" }}
                      disabled={isCategoryUpdating}
                      onClick={() => handleCategoryToggleAll(catName, true)}
                    >
                      Grant All
                    </button>
                    <button
                      type="button"
                      className="secondary"
                      style={{ fontSize: "0.75rem", padding: "3px 8px" }}
                      disabled={isCategoryUpdating}
                      onClick={() => handleCategoryToggleAll(catName, false)}
                    >
                      Revoke All
                    </button>
                  </div>
                </div>

                {!isCollapsed && (
                  <div
                    className="tool-category-items"
                    style={{
                      display: "grid",
                      gridTemplateColumns: "repeat(auto-fill, minmax(260px, 1fr))",
                      gap: "10px",
                      padding: "12px",
                    }}
                  >
                    {catTools.map((tool) => {
                      const isToolUpdating = updatingTool === tool.tool_name || isCategoryUpdating;
                      return (
                        <label
                          key={tool.tool_name}
                          className="tool-item-card"
                          style={{
                            display: "flex",
                            alignItems: "center",
                            gap: "10px",
                            padding: "10px 12px",
                            borderRadius: "6px",
                            background: "var(--surface-raised, rgba(255, 255, 255, 0.04))",
                            border: "1px solid var(--border-color, rgba(255, 255, 255, 0.08))",
                            cursor: isToolUpdating ? "default" : "pointer",
                            minWidth: 0,
                            boxSizing: "border-box",
                            opacity: isToolUpdating ? 0.6 : 1,
                          }}
                        >
                          <span className="switch-toggle compact">
                            <input
                              type="checkbox"
                              role="switch"
                              aria-label={tool.tool_name}
                              checked={tool.enabled}
                              disabled={isToolUpdating}
                              onChange={(e) => handleToolToggle(tool.tool_name, e.target.checked)}
                            />
                            <span className="switch-slider" />
                          </span>
                          <div style={{ display: "flex", flexDirection: "column", minWidth: 0, overflow: "hidden" }}>
                            <span
                              style={{
                                fontFamily: "monospace",
                                fontSize: "0.85rem",
                                color: tool.enabled ? "var(--text-primary, #fff)" : "var(--text-muted, #777)",
                                overflow: "hidden",
                                textOverflow: "ellipsis",
                                whiteSpace: "nowrap",
                              }}
                              title={tool.tool_name}
                            >
                              {tool.tool_name}
                            </span>
                            {workflowOverride && (
                              <span
                                style={{
                                  fontSize: "0.7rem",
                                  color: tool.effective_enabled ? "var(--color-success, #4caf50)" : "var(--color-danger, #ff5555)",
                                }}
                              >
                                {tool.effective_enabled ? "(effective: allowed)" : "(effective: blocked)"}
                              </span>
                            )}
                          </div>
                        </label>
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })
        )}

        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: "8px" }}>
          <button type="button" className="secondary" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </DialogShell>
  );
}
