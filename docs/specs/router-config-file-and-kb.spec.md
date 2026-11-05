# Spec: Router JSON Configuration File & Custom Knowledge Base Directory

- **ID**: `SPEC-ROUTER-CONFIG-001`
- **Status**: `Draft`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/router-config-file-and-kb.spec.ts`

---

## 1. Intent & User Outcome

Managing command-line flags for complex services becomes cumbersome and error-prone when orchestrating multi-agent systems, project-specific directories, and customized knowledge bases.

Users and developers need:
1. A single JSON configuration file (e.g. `router-config.json` or specified via `--config <path>`) supporting all router startup options with 2-space indented pretty-printing.
2. Safe relative path resolution where relative file and directory paths declared within the config JSON resolve relative to the configuration file's directory (or working directory when specified), preventing accidental leakage into unexpected parent paths.
3. Explicit CLI flag precedence so that any CLI flags passed on the command line cleanly override the corresponding values specified in the configuration file.
4. Flexible Knowledge Base (KB) configuration allowing a custom KB directory path to be specified either in the config file (`"kb": { "dir": "..." }`) or CLI flag, ensuring that agent KB tools (`kb_read`, `kb_write`, `kb_delete`, `kb_list`, `kb_search`) and HTTP file endpoints (`/v1/kb/file`) operate strictly within that isolated directory while preserving path traversal protections.

---

## 2. Core Invariants

1. **Deterministic Precedence**: Command-line flags ALWAYS take precedence over settings specified in the configuration file. Configuration file settings take precedence over built-in defaults and environment variables unless explicitly documented otherwise.
2. **Relative Path Anchoring**: Relative filesystem paths declared in the configuration file (e.g., `database`, `log`, `kb.dir`, `managed_ui_dist`, `codex_credential_path`) MUST resolve relative to the directory containing the configuration file, ensuring portable config files across working trees.
3. **Format & Pretty-Printing Guarantee**: Any configuration file written or exported by router tooling MUST be valid JSON formatted with 2-space indentation and a trailing newline for clean diffing and human readability.
4. **Knowledge Base Isolation**: When a custom KB directory is configured, all agent tools (`kb_read`, `kb_write`, `kb_delete`, `kb_list`, `kb_search`) and the HTTP handler (`/v1/kb/file`) MUST exclusively target that directory. Tool operations in one configured KB MUST NOT leak or cross-contaminate the global or default `~/.mecha-developer/kb/` directory.
5. **Path Traversal Containment**: Path sanitization (`SanitizeKBPath`) MUST remain strictly enforced against the active KB root, rejecting any attempts to traverse upwards outside the resolved KB root (e.g., `../`).
6. **Graceful Fallback & Validation**: If an invalid config file path is passed via `--config`, or the config contains malformed JSON or illegal option combinations, the router MUST fail fast with a descriptive error message and non-zero exit code before starting listeners or opening databases.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-CFG-001] Router launches using a JSON configuration file
- **User Goal**: Launch the mailbox router with options defined cleanly in a JSON file without passing dozens of command-line flags.
- **Given**:
  - A valid JSON config file `router-config.json` exists at `/home/wolfgang/project/config/router-config.json`.
  - The configuration file specifies:
    ```json
    {
      "database": "data/mailbox.db",
      "host": "127.0.0.1",
      "port": 8899,
      "log": "data/events.jsonl",
      "kb": {
        "dir": "docs/kb"
      }
    }
    ```
- **When**:
  - The operator starts `mailbox-router --config /home/wolfgang/project/config/router-config.json`.
- **Then**:
  - The router reads and parses `/home/wolfgang/project/config/router-config.json`.
  - The router resolves `database` to `/home/wolfgang/project/config/data/mailbox.db`.
  - The router resolves `log` to `/home/wolfgang/project/config/data/events.jsonl`.
  - The router binds to host `127.0.0.1` and port `8899`.
  - The router sets the active Knowledge Base root to `/home/wolfgang/project/config/docs/kb`.
  - HTTP `GET /v1/health` responds with status `200 OK`.

#### Scenario: [SCN-CFG-002] CLI flags override configuration file settings
- **User Goal**: Temporarily override a port or database path on the command line without editing the configuration file.
- **Given**:
  - A configuration file specifies `"port": 8899` and `"database": "data/mailbox.db"`.
- **When**:
  - The operator runs `mailbox-router --config router-config.json --port 9000`.
- **Then**:
  - The router binds to port `9000` instead of `8899`.
  - All other settings from the configuration file (e.g., `database`, `kb.dir`) remain active.

#### Scenario: [SCN-CFG-003] Relative path resolution anchored to config file directory
- **User Goal**: Share a project repository containing a config file and ensure paths work regardless of the shell's current working directory when invoking the router.
- **Given**:
  - A config file resides at `/workspace/app/config.json`.
  - Inside `config.json`, `"database"` is set to `"db/app.db"` and `"kb"` has `"dir": "knowledge"`.
  - The user's current working directory is `/tmp`.
- **When**:
  - The user executes `mailbox-router --config /workspace/app/config.json`.
- **Then**:
  - The database is opened at `/workspace/app/db/app.db`.
  - The KB root is established at `/workspace/app/knowledge`.
  - Files are NOT created under `/tmp/db/` or `/tmp/knowledge/`.

#### Scenario: [SCN-CFG-004] Agent tools and HTTP endpoint operate on custom KB directory
- **User Goal**: Use project-specific knowledge base documents isolated from user-wide KB storage.
- **Given**:
  - The router is configured with custom KB directory `/workspace/project-kb`.
  - File `/workspace/project-kb/architecture.md` exists with content `# Project Architecture`.
- **When**:
  - A user or frontend requests `GET /v1/kb/file?path=architecture.md`.
  - An embedded agent calls tool `kb_read` with `{"topic": "architecture"}`.
  - An embedded agent calls tool `kb_write` with `{"topic": "deployment", "content": "# Deployment Guide"}`.
- **Then**:
  - The HTTP request returns `# Project Architecture`.
  - `kb_read` returns `# Project Architecture`.
  - `kb_write` creates `/workspace/project-kb/deployment.md` and updates `/workspace/project-kb/INDEX.md`.
  - The default home directory KB (`~/.mecha-developer/kb/`) is not modified or read.

#### Scenario: [SCN-CFG-005] Config file export/generator outputs pretty-printed 2-space JSON
- **User Goal**: Inspect or generate a configuration template that is human-readable and clean in git diffs.
- **When**:
  - A configuration structure is serialized or generated.
- **Then**:
  - The resulting JSON is formatted with 2-space indentation.
  - Object keys are ordered logically.
  - The output terminates with a trailing newline.

---

### Non-Happy / Edge / Error Path Scenarios

#### Scenario: [SCN-CFG-006] Config file not found or inaccessible
- **User Goal**: Receive clear error feedback when pointing to a non-existent config file.
- **Given**:
  - No file exists at `/path/to/missing-config.json`.
- **When**:
  - The user runs `mailbox-router --config /path/to/missing-config.json`.
- **Then**:
  - The process exits immediately with a non-zero exit code.
  - The stderr output states that the configuration file could not be read or does not exist.

#### Scenario: [SCN-CFG-007] Malformed JSON in configuration file
- **User Goal**: Avoid starting the router in a corrupted or half-initialized state when syntax errors exist in the config.
- **Given**:
  - `router-config.json` contains trailing commas or unquoted keys (invalid JSON).
- **When**:
  - The user runs `mailbox-router --config router-config.json`.
- **Then**:
  - The process fails before opening any network listener or database connection.
  - A clear parse error citing the JSON syntax failure is printed to stderr.

#### Scenario: [SCN-CFG-008] Path traversal denial in custom KB directory
- **User Goal**: Ensure path traversal protection remains impenetrable even when using a custom KB root.
- **Given**:
  - The router is running with custom KB root `/workspace/project-kb`.
- **When**:
  - A client requests `GET /v1/kb/file?path=../../etc/passwd` or an agent invokes `kb_read` with `{"topic": "../secret"}`.
- **Then**:
  - The HTTP endpoint responds with HTTP `400 Bad Request` or `404 Not Found`.
  - The agent tool returns an error indicating path traversal is denied.
  - Files outside `/workspace/project-kb` are never accessed.

### Scenario: [SCN-CFG-009] Embedded KB root selection preserves supported paths
- **User Goal**: Resolve the active KB predictably without implicit access to retired storage.
- **Given**:
  - No custom router config or CLI KB directory is supplied.
- **When**:
  - Embedded KB tools resolve their storage root.
- **Then**:
  - A nonblank `MECHA_KB_DIR` takes precedence, with surrounding whitespace trimmed and the path cleaned, even if the directory does not exist.
  - Otherwise an existing `~/.mecha-developer/kb/` directory takes precedence over an existing `~/.mecha/kb/` directory.
  - If only `~/.mecha/kb/` exists, it remains the supported fallback.
  - If neither supported directory exists, the root is `~/.mecha-developer/kb/`; a whitespace-only environment value behaves as unset.
  - A file at either supported directory path is not treated as a directory.

### Scenario: [SCN-CFG-010] Unsupported sibling KB directory is ignored without changing its data
- **User Goal**: Stop implicit use of retired storage without losing documents.
- **Given**:
  - Only an unsupported sibling KB directory exists, containing documents.
  - `MECHA_KB_DIR` is unset or blank.
- **When**:
  - Embedded KB tools resolve their storage root.
- **Then**:
  - The root is `~/.mecha-developer/kb/`, not the unsupported directory.
  - Resolution does not create, delete, modify, or migrate any documents or directories.
  - If the supported `~/.mecha/kb/` fallback also exists, that fallback is selected instead.
  - An explicit `MECHA_KB_DIR` can still select any user-specified path; only implicit unsupported-directory discovery is removed.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-CFG-001` | Router launches using JSON config file | `tests/e2e/router-config-file-and-kb.spec.ts` | `launches router from json config with relative paths and custom kb` |
| `SCN-CFG-002` | CLI flags override configuration file | `tests/e2e/router-config-file-and-kb.spec.ts` | `cli flags override config file parameters` |
| `SCN-CFG-003` | Relative path resolution anchored to config | `tests/e2e/router-config-file-and-kb.spec.ts` | `resolves relative paths relative to config file directory` |
| `SCN-CFG-004` | Agent tools and HTTP operate on custom KB | `tests/e2e/router-config-file-and-kb.spec.ts` | `serves and writes kb documents strictly within configured kb root` |
| `SCN-CFG-005` | Pretty-printed 2-space indented JSON | `tests/e2e/router-config-file-and-kb.spec.ts` | `formats generated and exported config json with 2-space indentation` |
| `SCN-CFG-006` | Missing config file error | `tests/e2e/router-config-file-and-kb.spec.ts` | `fails immediately with clear error when config file does not exist` |
| `SCN-CFG-007` | Malformed JSON error | `tests/e2e/router-config-file-and-kb.spec.ts` | `fails immediately with parse error on malformed json config` |
| `SCN-CFG-008` | Path traversal denial in custom KB | `tests/e2e/router-config-file-and-kb.spec.ts` | `prevents path traversal out of custom kb directory` |
| `SCN-CFG-009` | Supported root selection and precedence | `tests/e2e/router-kb-root-selection.spec.ts` | `SCN-CFG-009: <case> (real embedded KB tools)` (parameterized cases) |
| `SCN-CFG-010` | Unsupported sibling ignored; data preserved | `tests/e2e/router-kb-root-selection.spec.ts` | `SCN-CFG-010: unknown sibling ignored without filesystem changes` |

The focused Playwright suite uses the existing embedded harness: isolated HOME,
real browser/WebSocket, REST through router-proxy, router and SQLite. Only the
external provider is mocked with queued native KB tool calls. Root-selection
cases also invoke read-only KB tools and compare filesystem snapshots. Supporting
Go tests `TestResolveKBRoot` and `TestKBTools_UnsupportedRootIgnored` cover direct
resolution and filesystem edge cases; they do not replace Playwright coverage.
