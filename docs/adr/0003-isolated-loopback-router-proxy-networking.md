# ADR 0003: Isolated Loopback & Router-Proxy Networking

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

`mecha-developer` provides real-time agent control, REST APIs, and WebSocket multiplexing. These services need to be accessed from multiple environments:
- Local processes, CLI tools, and test suites on the host workstation.
- Cross-origin web browsers running locally or remotely.
- Remote workstations and mobile browsers across a private Tailscale tailnet.

Exposing unauthenticated agent execution backends to 0.0.0.0 or public networks presents catastrophic security vulnerabilities (arbitrary code execution, filesystem reads/writes). Conversely, overly rigid CORS or binding policies break cross-origin local web development and tailnet access.

---

## Decision

Implement a multi-tier isolated networking topology centered on a loopback-bound Go router and a dedicated TCP reverse proxy:

1. **Loopback-Only Core Router**:
   - The primary router binds strictly to `127.0.0.1:8765`.
   - It is never exposed directly to external network interfaces.
2. **Dedicated TCP Router Proxy**:
   - For LAN/remote access, `router-proxy` forwards traffic to the upstream router (e.g. reflex upstream `192.168.1.100:8765`).
   - The proxy listens locally on `127.0.0.1:19876`.
   - Start pattern: `systemd-run --user --unit=router-proxy-reflex ... /path/to/mecha-developer/router-proxy/router-proxy --upstream-host 192.168.1.100 --upstream-port 8765 --listen-host 127.0.0.1 --listen-port 19876`.
3. **Tailscale Serve Integration**:
   - Tailscale Serve maps tailnet port `9876` to the local proxy: `sudo tailscale serve --tcp=9876 --bg tcp://127.0.0.1:19876`.
   - External tailnet hostname: `router.your-tailnet.ts.net:9876`.
   - Remote web clients point `localStorage.mecha.web.activeRouter` to this URL.
4. **Permissive Non-Credentialed CORS**:
   - The backend accepts cross-origin browser calls from arbitrary web origins.
   - CORS is treated strictly as browser transport compatibility, **not access control**.
   - Credentialed CORS (`Access-Control-Allow-Credentials: true`), cookies, and browser session authority are strictly forbidden.
   - Request bodies up to 6 MiB are supported to match payload limits.

---

## Invariants & Guarantees

- **No Public Bindings**: The core router never binds to public interfaces (`0.0.0.0`).
- **Tailnet Isolation**: Remote access is strictly confined to the trusted tailnet via Tailscale Serve. The router is unauthenticated; it must never be exposed to the public Internet.
- **WebSocket Parity**: `router-proxy` transparently upgrades and forwards WebSocket connections (`/v1/agents`) without altering frames or injecting unauthorized headers.
- **No Origin Authority**: Never authorize, allowlist, or log user identity based on browser `Origin` headers. Direct control calls must omit credentials.

---

## Consequences

### Positive
- Workstation remains secure from unauthenticated external access while enabling mobile/tailnet usability.
- Enables seamless local web development across different Vite/frontend ports without CORS blockage.
- Decouples core router lifecycle from host networking configurations.
- Transparent WebSocket proxying allows full real-time timeline streaming across machines.

### Negative
- Requires maintaining the `router-proxy` binary and Tailscale Serve port mappings.
- If Tailscale Serve configuration is misconfigured, tailnet connectivity drops until re-mapped.

---

## References

- Network Architecture Instructions: `/home/wolfgang/AGENTS.md`
- Router Proxy Documentation: `router-proxy/README.md`
- Operating Policy: `docs/wolfgang-agent-operating-policy.md`
- Tailscale Serve commands: `tailscale serve status`, `tailscale serve --tcp=9876 off`
