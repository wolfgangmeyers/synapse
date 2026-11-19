---
name: router-safe-upgrade
description: Perform zero-risk, crash-loop-safe upgrades of the mecha mailbox router with pre-flight diff analysis, isolated dry-runs, worker quiescence, and post-restart health checks.
---

# Router Safe Upgrade

Standard operating procedure for upgrading and restarting the mecha mailbox router. Designed for zero-risk, crash-loop-safe execution when operators or agents lack direct keyboard access to the host machine.

## Purpose & Guarantees

Upgrading the core mailbox router requires strict isolation and validation before touching the live process. A failing migration, broken startup hook, or crashing binary can sever communication with all running agents and lock out remote operators.

This workflow guarantees:
1. **Pre-flight visibility**: Audits exact commit deltas, identifying all migration, schema, store, and startup hook changes before building.
2. **Zero production risk during validation**: Candidate binaries and migrations run strictly in isolation against a sanitized database snapshot with parallel agents and goal loops disabled.
3. **Quiescent transitions**: Active worker turns, tool executions, and subagents settle before restart is triggered.
4. **Verified recovery**: Post-restart health checks and managed agent projections confirm all workers recover to `running` with `last_error: null`.

---

## Architecture & Lifecycle Reference

- **Upstream Router**: Listens on `127.0.0.1:8765` (or `0.0.0.0:8765`) and handles HTTP REST + WebSocket upgrades.
- **Supervisor Runner**: Managed via `tools/run_router.sh`. If the router process exits with code `75`, the script traps the exit, rebuilds the binary via `make -C router-proxy run-router` (running `build-router-local`), and relaunches the router automatically.
- **Controlled Restart Triggers**:
  - Agent tool: `mecha_restart_router(reason="<descriptive reason>")`
  - REST endpoint: `POST http://127.0.0.1:8765/v1/router/restart`
- **Safeguard against busy workers**: `mecha_restart_router` rejects restarts if sibling workers or subagents are actively executing turns, returning `[RESTART REJECTED - ACTIVE PROCESSES RUNNING]`. If forced override is explicitly needed within 60 seconds, `force=true` can be passed.

---

## Phase 1: Pre-flight Audit & Diff Analysis

Determine the baseline running state and audit every change between the running router and the candidate target commit.

### 1.1 Query Running State

Fetch current health from the live router:

```bash
LIVE_HEALTH=$(curl -fsS http://127.0.0.1:8765/v1/health)
RUNNING_COMMIT=$(echo "$LIVE_HEALTH" | jq -r '.runtime.source_commit')
SOURCE_TREE=$(echo "$LIVE_HEALTH" | jq -r '.runtime.source_tree')

echo "Running Commit: $RUNNING_COMMIT (tree: $SOURCE_TREE)"
```

Verify that the health check returns `"status": "ok"` and `"readiness": "ok"`.

### 1.2 Fetch & Inspect Commit Range

Fetch latest upstream changes and inspect the commit log:

```bash
git fetch origin main
TARGET_COMMIT=$(git rev-parse origin/main)

echo "Upgrade range: ${RUNNING_COMMIT}..${TARGET_COMMIT}"
git log --oneline "${RUNNING_COMMIT}..${TARGET_COMMIT}"
```

### 1.3 Identify High-Risk Surfaces

Inspect all changed files in the range:

```bash
git diff --stat "${RUNNING_COMMIT}..${TARGET_COMMIT}"
```

Specifically audit for changes in:
- **Database migrations**: `router-proxy/internal/store/migrations/*.sql`
  - Check whether new migrations add tables, modify columns, create triggers, or drop constraints.
  - Verify migration SQL syntax and backward-compatibility.
- **Store package**: `router-proxy/internal/store/`
  - Changes to SQL queries, table structs, migration loading logic, or transaction boundaries.
- **Startup hooks & runtime contracts**:
  - `router-proxy/cmd/mailbox-router/main.go` (CLI flags, environment variables, initialization sequence).
  - `router-proxy/internal/managed/` (session resume, process generation, recovery wakers).
  - `router-proxy/internal/transport/` (HTTP routes, WebSocket upgrade handlers).

If any breaking changes, flag deprecations, or irreversible data migrations are detected, confirm whether rollback is viable before proceeding.

---

## Phase 2: Isolated Dry-Run & Migration Verification

Validate candidate binary compilation, migration execution, and startup health on an isolated port using a sanitized copy of the live database.

### 2.1 Build Candidate Binary to Isolated Temporary Path

Build the candidate binary from the target tree without modifying the live deployed binary:

```bash
CANDIDATE_BIN="/tmp/test-router-upgrade-bin"
rm -f "$CANDIDATE_BIN"

make -C router-proxy build-router-local ROUTER_BINARY="$CANDIDATE_BIN"
chmod +x "$CANDIDATE_BIN"
```

Verify the binary was built cleanly:

```bash
"$CANDIDATE_BIN" --help >/dev/null 2>&1 || { echo "ERROR: Candidate binary failed basic execution test"; exit 1; }
```

### 2.2 Snapshot Live Database

Perform an online, atomic snapshot of the live SQLite database:

```bash
TEST_DB="/tmp/test-upgrade.sqlite"
rm -f "$TEST_DB" "${TEST_DB}-wal" "${TEST_DB}-shm"

LIVE_DB="${HOME}/.mecha-developer/router.sqlite"
sqlite3 "$LIVE_DB" "VACUUM INTO '${TEST_DB}'"
```

*Note: If `VACUUM INTO` encounters a lock timeout, retry after a few seconds or use `sqlite3 "$LIVE_DB" ".backup '${TEST_DB}'"`.*

### 2.3 Sanitize Database Snapshot (Guarantee Zero Parallel Execution)

**MANDATORY SAFETY STEP**: The dry-run instance must NEVER execute parallel agent turns, LLM calls, or goal loops against production workspaces.

Execute sanitation:

```bash
sqlite3 "$TEST_DB" "UPDATE managed_agent_policies SET goal_enabled = 0; UPDATE managed_agents SET desired_state = 'terminated';"
```

Verify zero agents or goals are active:

```bash
RUNNING_AGENTS=$(sqlite3 "$TEST_DB" "SELECT count(*) FROM managed_agents WHERE desired_state != 'terminated';")
ENABLED_GOALS=$(sqlite3 "$TEST_DB" "SELECT count(*) FROM managed_agent_policies WHERE goal_enabled != 0;")

if [[ "$RUNNING_AGENTS" -ne 0 || "$ENABLED_GOALS" -ne 0 ]]; then
  echo "FATAL: Database sanitation failed: running_agents=$RUNNING_AGENTS, enabled_goals=$ENABLED_GOALS"
  exit 1
fi
echo "Database sanitized successfully: 0 active agents, 0 enabled goals."
```

### 2.4 Launch Isolated Dry-Run Instance

Start the candidate router on an isolated port (e.g. `18765`) with isolated database and log files:

```bash
TEST_PORT=18765
TEST_LOG="/tmp/test-upgrade.jsonl"
TEST_OUT="/tmp/test-upgrade.stdout"
rm -f "$TEST_LOG" "$TEST_OUT"

nohup env MECHA_ROUTER_MANAGED=false "$CANDIDATE_BIN" \
  --host 127.0.0.1 \
  --port "$TEST_PORT" \
  --database "$TEST_DB" \
  --log "$TEST_LOG" \
  --managed-allow-launch-recipe-mismatch=true > "$TEST_OUT" 2>&1 &

TEST_PID=$!
echo "Isolated test router running on PID $TEST_PID (port $TEST_PORT)"
```

### 2.5 Verify Startup, Migrations, and Health

Poll the isolated health endpoint until ready:

```bash
curl --retry 15 --retry-delay 1 --retry-connrefused -fsS "http://127.0.0.1:${TEST_PORT}/v1/health" | jq .
```

Verify:
1. `"status": "ok"` and `"readiness": "ok"`.
2. All target schema migrations appear in `.schema_migrations`.
3. Check the test log for warnings or fatal errors:
   ```bash
   grep -iE "fatal|panic|error" "$TEST_LOG" || true
   ```

### 2.6 Clean Shutdown of Test Instance

Terminate the test router and clean up temporary test artifacts:

```bash
kill "$TEST_PID" && wait "$TEST_PID" 2>/dev/null || true
rm -f "$CANDIDATE_BIN" "$TEST_DB" "${TEST_DB}-wal" "${TEST_DB}-shm" "$TEST_LOG" "$TEST_OUT"
echo "Dry-run verified and cleaned up successfully."
```

---

## Phase 3: Worker Quiescence & Safe Restart

Ensure running workers are in a safe, non-active state before initiating the restart.

### 3.1 Check Active Worker State

Query the live router for active agents:

```bash
curl -fsS http://127.0.0.1:8765/v1/managed-agents | jq '[.agents[] | {
  agent_id,
  lifecycle_state,
  activity_state,
  goal_enabled: .goal_state.enabled,
  subagent_count: (.background_bash_jobs | length)
}]'
```

### 3.2 Broadcast Maintenance Notice (If Appropriate)

If workers are active or actively working toward goals, send a brief advance notice via mailbox so they do not start new expensive multi-turn tool flows:

```bash
python3 tools/mailbox_send.py \
  --from supervisor \
  --to worker-ticket-xyz \
  --subject "Router Upgrade Notice" \
  --body "Mailbox router upgrade and restart is scheduled in 1 minute. Please pause or finish active operations."
```

### 3.3 Poll Until Quiescence

Poll `/v1/managed-agents` until active workers settle into `idle` state and background subagents finish:

```bash
# Poll until no agent has activity_state == "running" or active subagents
while true; do
  BUSY=$(curl -fsS http://127.0.0.1:8765/v1/managed-agents | jq '[.agents[] | select(.activity_state != "idle")] | length')
  if [[ "$BUSY" -eq 0 ]]; then
    echo "All managed agents are quiescent (idle)."
    break
  fi
  echo "Waiting for $BUSY active worker(s) to settle..."
  sleep 3
done
```

### 3.4 Invoke Controlled Restart

Update the working tree or checkout if the runner builds from the local working directory:

```bash
# If running tools/run_router.sh from repo checkout, pull or checkout target commit
git pull --ff-only origin main
```

Trigger the restart using one of the two standard methods:

**Method A: Agent Tool (Preferred from managed session)**
Call `mecha_restart_router`:
```json
{
  "reason": "Safe upgrade to commit <target_commit> after isolated dry-run verification"
}
```
*Note: If sibling workers or subagents are active, the tool returns `[RESTART REJECTED - ACTIVE PROCESSES RUNNING]`. If you have confirmed quiescence or an urgent restart is justified, re-call within 60 seconds with `force: true`:*
```json
{
  "reason": "Forced upgrade to commit <target_commit>",
  "force": true
}
```

**Method B: REST Endpoint (Shell / Remote operator)**
Invoke the router restart endpoint:
```bash
curl -fsS -X POST http://127.0.0.1:8765/v1/router/restart | jq .
```

The router flushes the HTTP response (`{"ok": true, "restarting": true}`) and exits with code `75`. The supervisor runner script (`tools/run_router.sh`) traps exit code `75`, executes `make run-router` (rebuilding candidate binary), and resumes operation within ~1-2 seconds.

---

## Phase 4: Post-restart Health & Worker Recovery Verification

Verify that the router came back online, running the new commit, and all workers resumed cleanly.

### 4.1 Poll Live Health Endpoint

Poll the production port until the router is ready:

```bash
curl --retry 30 --retry-delay 1 --retry-connrefused -fsS "http://127.0.0.1:8765/v1/health" | jq .
```

Verify:
- `"status": "ok"`
- `"readiness": "ok"`
- `.runtime.source_commit` matches the newly deployed commit SHA.
- All new schema migrations are present in `.schema_migrations`.

### 4.2 Verify Managed Worker Recovery

Query the managed agent inventory:

```bash
AGENTS=$(curl -fsS http://127.0.0.1:8765/v1/managed-agents)
echo "$AGENTS" | jq '[.agents[] | {
  agent_id,
  lifecycle_state,
  activity_state,
  last_error,
  accepting_prompts
}]'
```

Verify:
1. For every agent that was running prior to restart:
   - `lifecycle_state == "running"`
   - `last_error == null`
   - `accepting_prompts == true`
2. No agents are stuck in `starting`, `failed`, or crash loops.

### 4.3 Inspect Router Log for Recovery Anomalies

Check the live JSONL log for unexpected errors:

```bash
tail -n 100 "${HOME}/.mecha-developer/router.jsonl" | grep -iE "fatal|panic|error" || echo "No startup errors detected."
```

---

## Rollback & Disaster Recovery

If the candidate router fails to start or encounters unrecoverable errors on live startup:

1. **Abort immediately**: Do not let crash-loops persist.
2. **Revert checkout**:
   ```bash
   git checkout "$RUNNING_COMMIT"
   ```
3. **Restore database snapshot** (if schema migration caused corruption):
   ```bash
   # Only if migration corrupted state or cannot run with older binary:
   # cp /path/to/backup.sqlite ~/.mecha-developer/router.sqlite
   ```
4. **Trigger restart**:
   ```bash
   curl -fsS -X POST http://127.0.0.1:8765/v1/router/restart || pkill -f mecha-mailbox-router
   ```
5. Confirm live health recovers to `$RUNNING_COMMIT`.
