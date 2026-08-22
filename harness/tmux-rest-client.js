export function createTmuxRestClient({ routerUrl = "http://127.0.0.1:8765", fetchImpl = fetch } = {}) {
  const base = routerUrl.replace(/\/$/, "");
  async function request(method, path, body) {
    const response = await fetchImpl(`${base}/v1/tmux/windows${path}`, {
      method,
      headers: body === undefined ? {} : { "content-type": "application/json" },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    let payload;
    try { payload = await response.json(); } catch { payload = undefined; }
    if (!response.ok) throw new Error(payload?.error?.code ?? "tmux_operation_failed");
    return payload;
  }
  return {
    list: () => request("GET", ""),
    provision: (name) => request("POST", "", { name }),
    keys: (name, keys) => request("POST", `/${encodeURIComponent(name)}/keys`, { keys }),
    remove: (name) => request("DELETE", `/${encodeURIComponent(name)}`),
    rename: (name, nextName) => request("PATCH", `/${encodeURIComponent(name)}`, { name: nextName }),
    action: (name, action, body = {}) => request("POST", `/${encodeURIComponent(name)}/${action}`, body),
  };
}
