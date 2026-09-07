# Mecha Router Proxy

Transparent HTTP and WebSocket reverse proxy for the Mailbox V2 router. It exposes the full router API on a configurable listen address (default `0.0.0.0`) and forwards all traffic to an upstream router over plain HTTP.

See `AGENTS.md` for how this fits into the mecha-developer system.

## Build and run the Mailbox router

The supported local launcher builds and runs the Go Mailbox router:

```bash
tools/run_router.sh
```

The equivalent Make command is:

```bash
make -C router-proxy run-router
```

The launcher uses `~/.mecha-developer/router.sqlite`, `~/.mecha-developer/router.jsonl`, `127.0.0.1`, port `8765`, and a 4096-character batch limit by default. Set `MECHA_ROUTER_DATABASE`, `MECHA_ROUTER_LOG`, `MECHA_ROUTER_HOST`, `MECHA_ROUTER_PORT`, or `MECHA_ROUTER_MAX_BATCH_CHARS` to override them. The event log rotates after 24 hours or 16 MiB and removes rotated files older than 48 hours; retained log files stay within 32 MiB.

The launcher also enables the read-only tmux/router audit. It checks the existing `mecha` tmux session every 30 minutes and durably sends a summary to `supervisor` when it finds unmapped windows or live agents without a goal or recurring reminder. It never provisions tmux. Set `MECHA_ROUTER_AUDIT_SUPERVISOR_TARGET`, `MECHA_ROUTER_AUDIT_TMUX_SESSION`, or `MECHA_ROUTER_POLICY_TIMEOUT` to override the delivery target, observed session, or per-run policy timeout. Only one unsettled audit message is retained, so changed findings do not build a queue while the target is away.

Managed Pi launch is opt-in and remains disabled unless `MECHA_ROUTER_MANAGED=true` is set. Enabled mode needs a usable Pi executable, managed home and contained session root, generated manifest, mecha-rpc and pi-subagents entry files, allowed working-directory roots, and a quiet recipient. It uses the static `openai-codex/gpt-5.6-terra` catalog. The launcher creates the managed home and session directories with mode `0700`; it does not create tmux windows. Worker routes are locally unauthenticated direct controls for callers that can reach the router. They do not use a UI session, CSRF token, helper, Cursor extension, built UI, version pin, or content hash.

## Build and run the proxy

```bash
go build -o router-proxy ./router-proxy
go run ./router-proxy \
  --upstream-host 127.0.0.1 \
  --upstream-port 8765 \
  --listen-port 9876
```

| Flag | Default | Purpose |
|------|---------|---------|
| `--upstream-host` | required | Router host to connect to |
| `--upstream-port` | required | Router port to connect to |
| `--listen-port` | required | Port exposed on this machine |
| `--listen-host` | `0.0.0.0` | Bind address |

On startup:

```
MECHA_ROUTER_PROXY_READY listen=http://0.0.0.0:9876 upstream=http://127.0.0.1:8765
```

## Local parity test

Terminal 1 — router:

```bash
tools/run_router.sh
```

Terminal 2 — proxy:

```bash
go run ./router-proxy \
  --upstream-host 127.0.0.1 --upstream-port 8765 \
  --listen-port 9876
```

Terminal 3 — verify:

```bash
curl -s http://127.0.0.1:8765/v1/health
curl -s http://127.0.0.1:9876/v1/health
```

Point clients at the proxy with `MECHA_ROUTER_URL=http://127.0.0.1:9876`.

## Notes

- WebSocket upgrades on `/v1/agents` are forwarded without injecting an `Origin` header.
- Request bodies up to 6 MiB are supported to match the router limit.
- Pi extension clients require a loopback hostname in `MECHA_ROUTER_URL`; use `127.0.0.1` for local testing through a different port.
