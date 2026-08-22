"""Small synchronous client for the router's authoritative tmux window API."""
import json
import os
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen

DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"

class TmuxRestError(RuntimeError):
    def __init__(self, status, code):
        self.status = status
        self.code = code
        super().__init__(f"{code} ({status})")

class TmuxRestClient:
    def __init__(self, router_url=None, opener=urlopen):
        self.router_url = (router_url or os.environ.get("MECHA_ROUTER_URL") or DEFAULT_ROUTER_URL).rstrip("/")
        self.opener = opener

    def request(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = Request(f"{self.router_url}/v1/tmux/windows{path}", data=data, method=method,
                          headers={"Content-Type": "application/json"} if data else {})
        try:
            with self.opener(request, timeout=5) as response:
                raw = response.read()
                return response.status, (json.loads(raw) if raw else None)
        except HTTPError as error:
            try:
                payload = json.loads(error.read())
                code = payload.get("error", {}).get("code", "tmux_operation_failed")
            except (OSError, ValueError, TypeError):
                code = "tmux_operation_failed"
            raise TmuxRestError(error.code, code) from error
        except (OSError, URLError) as error:
            raise TmuxRestError(503, "tmux_unavailable") from error

    def list(self):
        status, payload = self.request("GET", "")
        return payload.get("windows", []) if isinstance(payload, dict) else []

    def provision(self, name, session_id=None):
        body = {"name": name}
        if session_id is not None:
            body["session_id"] = session_id
        return self.request("POST", "", body)[1]

    def keys(self, name, keys):
        return self.request("POST", f"/{quote(name, safe='')}/keys", {"keys": keys})[1]

    def remove(self, name):
        self.request("DELETE", f"/{quote(name, safe='')}")

    def rename(self, name, next_name):
        return self.request("PATCH", f"/{quote(name, safe='')}", {"name": next_name})[1]

    def action(self, name, action, body=None):
        return self.request("POST", f"/{quote(name, safe='')}/{action}", body or {})[1]

    def exists(self, name):
        return any(isinstance(window, dict) and window.get("name") == name for window in self.list())
