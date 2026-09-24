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

Router-managed Pi launch is enabled by default. Set `MECHA_ROUTER_MANAGED=false` to run the legacy router without managed-worker routes; other values are rejected before startup. Enabled mode needs a usable Pi executable, managed home and contained session root, generated manifest, mecha-rpc and pi-subagents entry files, allowed working-directory roots, and a quiet recipient. The normal launcher allows the current user's home directory, so local project folders are selectable without opening the rest of the filesystem. Set `MECHA_ROUTER_MANAGED_CWD_ROOTS` only when a different explicit scope is required. Its built-in fallback catalog uses `openai-codex/gpt-5.6-sol` and makes `openai-codex/gpt-6-astra` available for larger or more difficult tasks. The launcher creates the managed home and session directories with mode `0700`; it does not create tmux windows. Worker routes are locally unauthenticated direct controls for callers that can reach the router. They do not use a UI session, CSRF token, helper, Cursor extension, built UI, version pin, or content hash.

## Stop and restart inventory

The historical sender of the issue #31 `SIGTERM` is unknown.

Production paths:

| Path | Stop or restart behavior |
|------|--------------------------|
| `router-proxy/cmd/mailbox-router/main.go` | Handles `SIGINT` and `SIGTERM`, then calls `Router.Close`. |
| `router-proxy/internal/transport/router.go` | `Router.Close` calls `Manager.Close`, which performs the durable managed-child shutdown. |
| `router-proxy/cmd/managed-acceptance-router/main.go` | Acceptance-only router with the same signal-to-close behavior. It is not installed as a service. |
| `router-proxy/main.go` | Handles signals for the optional reverse proxy and closes that proxy's HTTP server. It never stops or restarts the upstream router. |
| `tools/run_router.sh` and `make -C router-proxy run-router` | Start the production router in the foreground. They contain no stop or restart action. |

Process-level test and acceptance paths:

| Path | Stop or restart behavior |
|------|--------------------------|
| `router-proxy/cmd/mailbox-router/main_test.go` | Sends `SIGTERM` to an isolated built router and reopens the same private database for ordering coverage. |
| `web/router-ui-react/tests/support/isolated-go-router.mjs` | Owns `stopRouterProcess`, isolated-router restart, escalation, port checks, and task-root removal for tests. |
| `web/router-ui-react/tests/functional/managed-go-router.mjs` | Uses `stopRouterProcess` to restart and close its functional-test router. |
| `web/router-ui-react/tests/acceptance/bcb-runner.mjs` | Closes routers created by `isolated-go-router.mjs`; its local `stopChild` handles only task Vite, BCB, Chromium, and comparison processes. |
| `web/router-ui-react/tests/acceptance/bcb-managed-rpc-release-runner.mjs` | Stops and replaces its isolated managed acceptance router. It can also stop that router during cleanup. |
| `web/router-ui-react/tests/acceptance/bcb-managed-recovery-runner.mjs` and `managed-recovery-harness.mjs` | Send the measured `SIGTERM`, replace only that trial's router, enforce the absolute deadline, capture failure evidence, and clean isolated resources. |

The following in-process transport test files call `Router.Close` on routers they construct: `connected_agent_snapshot_behavioral_test.go`, `delivery_coalescing_test.go`, `domain1_behavioral_functional_test.go`, `lifecycle_test.go`, `managed_disabled_test.go`, `managed_goal_evaluator_native_test.go`, `managed_history_retention_behavioral_test.go`, `managed_http_test.go`, `managed_inline_allowance_native_test.go`, `managed_launch_behavioral_functional_test.go`, `managed_launch_http_test.go`, `managed_launch_validation_behavioral_test.go`, `managed_native_test.go`, `managed_reset_native_test.go`, `managed_root_control_native_test.go`, `managed_termination_native_test.go`, `mixed_managed_legacy_behavioral_test.go`, `phase3a_composed_test.go`, `policy_test.go`, `reminder_lifecycle_behavioral_functional_test.go`, `root_adapters_behavioral_functional_test.go`, `startup_readiness_test.go`, `transport_test.go`, and `ui_live_test.go` under `router-proxy/internal/transport/`. They do not signal or restart an external router.

There is no checked-in router service definition or service-manager stop command. The documented `launchctl kickstart gui/$UID/<label>` command starts or recovers a launchd job. The documented `launchctl kickstart -k gui/$UID/<label>` command is an explicit operator repair that may replace the job after a failed health check. Neither command identifies the historical signal sender. Ordinary operator shutdown is an OS `SIGINT` or `SIGTERM` delivered to the foreground `mailbox-router`; there is no repository wrapper for it.

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
