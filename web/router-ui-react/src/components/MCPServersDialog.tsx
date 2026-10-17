import { useEffect, useState, type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient, MCPServer } from "../transport/http-client";

export interface MCPServersDialogProps {
  open: boolean;
  client: RouterHttpClient;
  onClose: () => void;
}

export function MCPServersDialog({ open, client, onClose }: MCPServersDialogProps): ReactElement {
  const [mode, setMode] = useState<"list" | "add">("list");
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState<string | null>(null);

  // New server form state
  const [url, setUrl] = useState("");
  const [name, setName] = useState("");
  const [adding, setAdding] = useState(false);
  const [syncingId, setSyncingId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [authenticatingId, setAuthenticatingId] = useState<string | null>(null);

  const loadServers = async (): Promise<void> => {
    setLoading(true);
    setError(null);
    try {
      const list = await client.listMCPServers();
      setServers(list);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load MCP servers");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (open) {
      setMode("list");
      void loadServers();
    } else {
      setMode("list");
      setError(null);
      setStatusMessage(null);
      setUrl("");
      setName("");
    }
  }, [open]);

  // Listen for OAuth message event from callback popup window
  useEffect(() => {
    const handleMessage = (event: MessageEvent): void => {
      if (event.data && typeof event.data === "object" && event.data.type === "mcp_oauth_complete") {
        setAuthenticatingId(null);
        if (event.data.success) {
          setStatusMessage("OAuth authorization completed successfully.");
        } else {
          setError(typeof event.data.message === "string" ? event.data.message : "OAuth authorization failed.");
        }
        void loadServers();
      }
    };

    window.addEventListener("message", handleMessage);
    return () => {
      window.removeEventListener("message", handleMessage);
    };
  }, []);

  const handleAddServer = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault();
    const trimmedUrl = url.trim();
    if (!trimmedUrl) return;

    setAdding(true);
    setError(null);
    setStatusMessage(null);

    try {
      const res = await client.addMCPServer({
        url: trimmedUrl,
        ...(name.trim() ? { name: name.trim() } : {}),
        auth_type: "oauth",
      });

      setUrl("");
      setName("");
      setMode("list");

      if (res.auth_url) {
        setAuthenticatingId(res.server.id);
        window.open(res.auth_url, "mcp_oauth", "width=600,height=700,status=no,resizable=yes");
      } else {
        setStatusMessage(`Added MCP server "${res.server.name}".`);
      }

      await loadServers();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add MCP server");
    } finally {
      setAdding(false);
    }
  };

  const handleStartOAuth = async (server: MCPServer): Promise<void> => {
    setError(null);
    setStatusMessage(null);
    setAuthenticatingId(server.id);

    try {
      const { authorization_url } = await client.startMCPOAuth(server.id);
      if (authorization_url) {
        window.open(authorization_url, "mcp_oauth", "width=600,height=700,status=no,resizable=yes");
      } else {
        setError("No authorization URL returned from router.");
        setAuthenticatingId(null);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to initiate OAuth");
      setAuthenticatingId(null);
    }
  };

  const handleSync = async (server: MCPServer): Promise<void> => {
    setSyncingId(server.id);
    setError(null);
    setStatusMessage(null);

    try {
      const res = await client.syncMCPServer(server.id);
      if (res.error) {
        const errMsg = typeof (res.error as { message?: string })?.message === "string"
          ? (res.error as { message: string }).message
          : "Sync failed";
        setError(`Sync failed: ${errMsg}`);
      } else {
        setStatusMessage(`Synced tools for "${server.name}".`);
      }
      await loadServers();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to sync MCP server");
    } finally {
      setSyncingId(null);
    }
  };

  const handleDelete = async (server: MCPServer): Promise<void> => {
    if (!window.confirm(`Delete MCP server "${server.name}"?`)) {
      return;
    }

    setDeletingId(server.id);
    setError(null);
    setStatusMessage(null);

    try {
      await client.deleteMCPServer(server.id);
      setStatusMessage(`Deleted MCP server "${server.name}".`);
      await loadServers();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to delete MCP server");
    } finally {
      setDeletingId(null);
    }
  };

  const formatLastSync = (lastSyncAt?: string): string => {
    if (!lastSyncAt) return "Never";
    try {
      const d = new Date(lastSyncAt);
      if (Number.isNaN(d.getTime())) return "Never";
      return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit", second: "2-digit" });
    } catch {
      return "Never";
    }
  };

  const renderBadge = (status: string): ReactElement => {
    switch (status) {
      case "connected":
        return <span className="mcp-server-badge connected">Connected</span>;
      case "needs_auth":
        return <span className="mcp-server-badge needs-auth">Needs Auth</span>;
      case "error":
        return <span className="mcp-server-badge error">Error</span>;
      default:
        return <span className="mcp-server-badge default">{status || "Unknown"}</span>;
    }
  };

  return (
    <DialogShell
      open={open}
      title={mode === "add" ? "Add MCP Server" : "MCP Servers"}
      titleId="mcp-servers-title"
      description={
        mode === "add"
          ? "Configure a new Model Context Protocol endpoint."
          : "Connect remote MCP servers to distribute tools to agents."
      }
      onClose={onClose}
      closeLabel="Close MCP servers"
      className="message-dialog mcp-servers-dialog"
    >
      <div className="mcp-servers-container">
        {statusMessage ? (
          <div className="mcp-server-alert success" role="status">
            <i className="fa-solid fa-circle-check" aria-hidden="true" />
            <span>{statusMessage}</span>
          </div>
        ) : null}

        {error ? (
          <div className="mcp-server-alert error" role="alert">
            <i className="fa-solid fa-triangle-exclamation" aria-hidden="true" />
            <span>{error}</span>
          </div>
        ) : null}

        {mode === "add" ? (
          /* Add Server Form */
          <section className="mcp-add-section">
            <form className="mcp-add-form" onSubmit={(e) => void handleAddServer(e)}>
              <div className="mcp-form-inputs">
                <div className="mcp-form-field">
                  <label htmlFor="mcp-server-url-input">Server URL</label>
                  <input
                    id="mcp-server-url-input"
                    type="url"
                    className="mcp-url-input"
                    placeholder="https://podclave.com/mcp"
                    value={url}
                    onChange={(e) => setUrl(e.target.value)}
                    required
                    aria-label="Server URL"
                    autoFocus
                  />
                </div>
                <div className="mcp-form-field">
                  <label htmlFor="mcp-server-name-input">Server Name (optional)</label>
                  <input
                    id="mcp-server-name-input"
                    type="text"
                    className="mcp-name-input"
                    placeholder="e.g. Linear, GitHub"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    aria-label="Server name"
                  />
                </div>
              </div>
              <div className="mcp-form-actions">
                <button
                  type="button"
                  className="secondary"
                  onClick={() => {
                    setError(null);
                    setMode("list");
                  }}
                >
                  Cancel
                </button>
                <button
                  type="submit"
                  className="btn btn-primary mcp-add-btn"
                  disabled={adding || !url.trim()}
                >
                  {adding ? (
                    <>
                      <i className="fa-solid fa-arrows-rotate fa-spin" aria-hidden="true" /> Adding...
                    </>
                  ) : (
                    <>
                      <i className="fa-solid fa-key" aria-hidden="true" /> Authorize &amp; Add
                    </>
                  )}
                </button>
              </div>
            </form>
          </section>
        ) : (
          /* Configured MCP Servers List */
          <section className="mcp-list-section">
            <div className="mcp-list-header">
              <h3>Configured Servers ({servers.length})</h3>
              <div className="mcp-header-actions">
                <button
                  type="button"
                  className="icon-btn-round"
                  title="Refresh servers"
                  aria-label="Refresh servers"
                  onClick={() => void loadServers()}
                  disabled={loading}
                >
                  <i className={`fa-solid fa-arrows-rotate ${loading ? "fa-spin" : ""}`} aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="btn btn-primary mcp-add-btn"
                  onClick={() => {
                    setError(null);
                    setStatusMessage(null);
                    setMode("add");
                  }}
                >
                  + Add Server
                </button>
              </div>
            </div>

            {servers.length === 0 ? (
              <div className="mcp-empty-state">
                {loading ? "Loading MCP servers..." : "No MCP servers configured yet. Click \"+ Add Server\" above to connect one."}
              </div>
            ) : (
              <div className="mcp-server-cards">
                {servers.map((server) => {
                  const isSyncing = syncingId === server.id;
                  const isDeleting = deletingId === server.id;
                  const isAuthenticating = authenticatingId === server.id;

                  return (
                    <div key={server.id} className="mcp-server-card">
                      <div className="mcp-server-main">
                        <div className="mcp-server-title-line">
                          <strong className="mcp-server-name">{server.name}</strong>
                          {renderBadge(server.status)}
                        </div>
                        <code className="mcp-server-url" title={server.url}>
                          {server.url}
                        </code>
                        <div className="mcp-server-meta">
                          <span className="mcp-meta-item">
                            <i className="fa-solid fa-wrench" aria-hidden="true" />
                            <span>{server.tools_count} {server.tools_count === 1 ? "tool" : "tools"}</span>
                          </span>
                          <span className="mcp-meta-item">
                            <i className="fa-solid fa-clock" aria-hidden="true" />
                            <span>Last sync: {formatLastSync(server.last_sync_at)}</span>
                          </span>
                          {server.auth_type && server.auth_type !== "none" ? (
                            <span className="mcp-meta-item mcp-auth-type">
                              <i className="fa-solid fa-key" aria-hidden="true" />
                              <span>{server.auth_type.toUpperCase()}</span>
                            </span>
                          ) : null}
                        </div>
                        {server.last_error ? (
                          <div className="mcp-server-error-text" title={server.last_error}>
                            <i className="fa-solid fa-circle-exclamation" aria-hidden="true" />
                            <span>{server.last_error}</span>
                          </div>
                        ) : null}
                      </div>

                      <div className="mcp-server-actions">
                        {server.auth_type === "oauth" && server.status === "needs_auth" ? (
                          <button
                            type="button"
                            className="mcp-action-btn-auth"
                            title="Authorize server via OAuth"
                            aria-label={`Authorize ${server.name}`}
                            onClick={() => void handleStartOAuth(server)}
                            disabled={isAuthenticating}
                          >
                            <i
                              className={`fa-solid ${isAuthenticating ? "fa-arrows-rotate fa-spin" : "fa-arrow-up-right-from-square"}`}
                              aria-hidden="true"
                            />
                            <span>Authorize</span>
                          </button>
                        ) : null}

                        <button
                          type="button"
                          className="icon-btn-round"
                          title="Sync tools"
                          aria-label={`Sync tools for ${server.name}`}
                          onClick={() => void handleSync(server)}
                          disabled={isSyncing || isDeleting}
                        >
                          <i className={`fa-solid fa-arrows-rotate ${isSyncing ? "fa-spin" : ""}`} aria-hidden="true" />
                        </button>

                        <button
                          type="button"
                          className="icon-btn-round danger"
                          title="Delete server"
                          aria-label={`Delete ${server.name}`}
                          onClick={() => void handleDelete(server)}
                          disabled={isDeleting || isSyncing}
                        >
                          <i className="fa-solid fa-trash" aria-hidden="true" />
                        </button>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </section>
        )}
      </div>
    </DialogShell>
  );
}
