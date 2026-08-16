# Mecha Router Proxy

Transparent HTTP and WebSocket reverse proxy for the Mailbox V2 router. It exposes the full router API on a configurable listen address (default `0.0.0.0`) and forwards all traffic to an upstream router over plain HTTP.

See `AGENTS.md` for how this fits into the mecha-developer system.

## Build

```bash
go build -o router-proxy ./router-proxy
```

## Run

```bash
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
node tools/run_router.mjs --database ~/.mecha-developer/router.sqlite
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
