import { useState, type ReactElement } from "react";
interface Tool { tool_name: string; category: string; enabled: boolean; effective_enabled?: boolean }
interface Props { tools: Tool[]; workflowOverride?: boolean; updatingCategory: string | null; updatingTool: string | null; onCategoryToggle: (category: string, enabled: boolean) => void; onToolToggle: (name: string, enabled: boolean) => void; mode: "agent" | "global" }
export function ToolCategoryList({ tools, workflowOverride, updatingCategory, updatingTool, onCategoryToggle: handleCategoryToggleAll, onToolToggle: handleToolToggle, mode }: Props): ReactElement {
  const [collapsedCategories, setCollapsedCategories] = useState<Record<string, boolean>>({});
  const toggleCategoryCollapse = (category: string) => setCollapsedCategories(prev => ({ ...prev, [category]: !prev[category] }));
  const map = new Map<string, Tool[]>();
  for (const tool of tools) { const category = tool.category || "General"; map.set(category, [...(map.get(category) ?? []), tool]); }
  const categories = Array.from(map.entries()).sort(([a], [b]) => a.localeCompare(b));
  return <>{          categories.map(([catName, catTools]) => {
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
                      {grantedCount} / {catTools.length} {mode === "agent" ? "granted" : "enabled"}
                    </span>
                  </div>
                  {<div
                    style={{ display: "flex", gap: "6px" }}
                    onClick={(e) => e.stopPropagation()}
                  >
                    <button
                      type="button"
                      className="secondary"
                      style={{ fontSize: "0.75rem", padding: "3px 8px" }}
                      disabled={Boolean(updatingCategory || updatingTool)}
                      onClick={() => handleCategoryToggleAll(catName, true)}
                    >
                      {mode === "agent" ? "Grant All" : "Enable All"}
                    </button>
                    <button
                      type="button"
                      className="secondary"
                      style={{ fontSize: "0.75rem", padding: "3px 8px" }}
                      disabled={Boolean(updatingCategory || updatingTool)}
                      onClick={() => handleCategoryToggleAll(catName, false)}
                    >
                      {mode === "agent" ? "Revoke All" : "Disable All"}
                    </button>
                  </div>}
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
                      const isToolUpdating = Boolean(updatingTool || updatingCategory);
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
          })}</>;
}
