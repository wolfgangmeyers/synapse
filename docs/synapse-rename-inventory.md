# Synapse rename inventory

Inventory only. No rename or migration is authorized by this file.

## Scope and result

Snapshot: `main`, HEAD `34c03784264c220455c8c294f3da05b91a07dc66`, scanned 2026-11-11. Working-tree contents, including existing edits, are authoritative for these line numbers.

- 2,901 tracked files plus two relevant untracked files: 2,903 total.
- 1,904 UTF-8 text files and 999 binary files. No missing files or tracked symlinks.
- **5,947 occurrence records in 1,010 files**: 5,890 text matches on 3,865 distinct lines, plus 57 matches in paths. No matching printable binary metadata found.
- Text matches span 1,004 files. Counts include deliberately broad personal/external names and incidental substring matches. They are not a count of required edits.
- [Complete occurrence appendix](synapse-rename-occurrences.tsv): every match, not representative samples. The action tables below classify the work. Use both together.
- Only this Markdown file and the TSV are task outputs. No production edits, live state access, service changes, or builds. A later validation pass is authorized to commit/push only these inventory outputs.

### Appendix format

Tab-separated columns: `file`, `line`, `column_or_byte_offset`, `kind`, `scope`, `domain`, `match`, `context_json`.

Each row is one match. Text line/column are 1-based Unicode positions. Path records use line `0` and 1-based path positions. Binary metadata records, if present, would use line `0` and a 0-based byte offset. Context is a JSON string containing the exact match with up to 100 characters on each side (90 for binary metadata). Long generated lines are intentionally not copied in full. Multiple matches on one line have separate rows, so no occurrence is hidden by truncation. Decode `context_json` rather than treating escapes as source text.

Domain labels describe location, not edit permission. `go-router`, `android`, `web`, and `auth-helper` include their tests. `generated-web` is tracked bundled output. `docs-build-root` includes root plans, build files, and documentation.

| Domain | Occurrence records |
|---|---:|
| Go router | 3,615 |
| Docs, build, root | 954 |
| Web source and tests | 478 |
| Top-level tests | 375 |
| Android | 162 |
| Agent tooling and dotfiles | 147 |
| Auth helper | 85 |
| Tracked generated web | 67 |
| Legacy protocol and contracts | 49 |
| Desktop | 15 |
| **Total** | **5,947** |

## Proposed convention

These are proposed targets, not resolved compatibility decisions.

| Surface | Proposed form | Constraint |
|---|---|---|
| Display name | `Synapse` | Replace product-facing Mecha, Mecha-Developer, and project-name Mecha-Wolfgang. Do not rename the person Wolfgang. |
| Repository/check-out slug | `synapse` | Confirm owner and remote destination first. |
| npm package identifiers | `synapse-mailbox-v2`, `@synapse/router-ui-react`, `@synapse/managed-auth-helper` | Check namespace ownership/publication before external use. Private local packages still need lockfile updates. |
| Environment prefix | `SYNAPSE_` | Prefer aliases with explicit precedence over a breaking cutover. |
| Tool names | `synapse_<capability>` | Requires capability, authorization, workflow, history, and client migration. Keeping legacy tool IDs is a valid initial choice. |
| Protocol namespace | `synapse.<domain>/vN` | Do not silently relabel existing versioned protocols. Negotiate or retain old values. |
| Storage namespace | `synapse.*`, `synapse-router-web-ui`, `~/.synapse/` | Requires migration or deliberate retention of old names. |
| Android/macOS identifier | Candidate `life.meyers.synapse` | Installed-app identity is separately gated from visible branding. |
| Go module | Candidate `github.com/<confirmed-owner>/synapse/router-proxy` | Current module owner differs from current Git remote owner. Resolve that before imports. |
| Assets/internal symbols | `synapse-icon.png`, `synapse-watermark.png`, `Synapse.icns`, `Theme.Synapse`, `SynapseApp` | Only if branding decision requires it. Generic robots/mechs are not inherently old branding. |

### Decisions required before implementation

1. Branding-only first release, or simultaneous compatibility migration? Branding-only can leave opaque IDs unchanged.
2. Keep Android `applicationId` and Electron bundle ID for upgrades? Changing them creates a different installed app and may isolate existing state, signing, notifications, and OS registrations.
3. Keep `~/.mecha-developer` as the canonical state directory initially, or migrate? Define precedence when both old and new roots exist. Never create an empty second database by accident.
4. Keep protocol/tool/export names indefinitely, accept aliases, or introduce a version? Define mixed-client behavior and rollback before changing producers.
5. Confirm remote owner, GitHub rename, package namespace ownership, and distribution destinations. No external names were checked for availability.
6. Define an alias deprecation window and bounded migration bookkeeping. Never store unbounded migration history.
7. Keep cryptographic and deterministic-ID namespaces unchanged unless a separate versioned data migration is implemented.

## Action inventory

Actions apply to **all corresponding appendix rows**, not only the entry points cited here. A file can contain multiple classes. Classify each occurrence by meaning before editing.

### A. Branding: ordinary source changes after scope approval

| Locations / entry points | Required consideration | Acceptance check for later work |
|---|---|---|
| `README.md`, `AGENTS.md`, `CLAUDE.md`, `PRD.md`, `PLAN.md`, `SPEC.md`, root `*_PLAN.md`, `docs/**`, `.claude/skills/**` | Replace current product titles and current instructions. Distinguish historical evidence and referenced sibling projects. Fix example paths only when they describe the new installation. | Current instructions are executable. Historical records still identify the artifact actually tested. |
| `web/router-ui-react/index.html:6-8`, `src/components/BrandHeader.tsx:24`, `src/App.tsx`, `ConversationHeader.tsx`, `ToolPermissionsDialog.tsx`, `src/transport/router-transport.ts` | Page title, brand header, connection/loading copy, tool explanation copy, diagnostics. | Browser title, connection/error screens, main header, permissions text all say Synapse. |
| `app/src/main/AndroidManifest.xml:4`, `res/values/styles.xml:1`, `MainActivity.kt` | App label, theme name, Compose function name, visible headings and reconnect text. Package identity is separate (B). | Launcher and in-app display change without losing installed data. |
| `desktop/electron/config.mjs:4`, `main.mjs:43,51`, root `package.json` | `APP_NAME`, product name, startup errors. Electron app name can also affect default user-data paths: inspect before treating this as cosmetic. | Window/app/package labels and errors agree. Existing profile is retained or migrated. |
| `router/contract/managed-tools-v1.json`, `router-proxy/internal/managed/**` | Human-readable labels, prompt snippets, instructions, errors. Labels inside hashed tool definitions are not purely cosmetic (C). | New labels do not invalidate definition identities unexpectedly. |
| CSS `.mecha-watermark`, image URLs, resource names | Internal selector/asset references may be renamed atomically, or retained as opaque internals. | No missing images, selector drift, or accessibility regressions. |

### B. Packaging, module paths, installed identities

| Locations | Action / risk |
|---|---|
| Root `package.json`, `package-lock.json`; `web/router-ui-react/package.json`, `package-lock.json`; `auth-helper/package.json`, `package-lock.json` | Update package names and root lock metadata together. Do not rewrite third-party package names or URLs. |
| `desktop/electron/config.mjs:5,12-13`, `package.mjs`, `desktop/electron/assets/Mecha.icns`, `tests/electron_desktop.test.mjs`, `docs/specs/desktop-package-payload.spec.md` | Decide bundle ID independently. Update package payload allowlist, icon paths, staging prefixes, distribution paths, and `MECHA_DESKTOP_PACKAGE_READY` consumers together. Inspect packaged `Info.plist` and executable/app directory names after packaging. |
| `settings.gradle.kts:3`, `app/build.gradle.kts:11,15,20` | Gradle root project, namespace, application ID, instrumentation runner. Changing namespace need not imply changing installed application ID. |
| `app/src/{main,test,androidTest}/java/life/meyers/mecha/**` | Appendix path records enumerate all affected Kotlin paths. Coordinate package declarations, imports, runner reference, tests, manifest/resource references. All 50 Kotlin path matches must be considered, not just `MainActivity.kt`. |
| `router-proxy/go.mod:1` and every matching Go import | Current module: `github.com/reflexivity/mecha-developer/router-proxy`. Current remote: `git@github.com:wolfgangmeyers/mecha-developer.git`. Do not infer ownership by replacement. Change module and all owned imports together only after choosing the canonical path. Check `go.sum` without rewriting unrelated dependencies. |
| `router-proxy/Makefile`, `tools/run_router.sh`, `tools/run_reflex_proxy.sh`, `router-proxy/main.go`, `cmd/mailbox-router/main.go` | Binary default `~/.mecha-developer/bin/mecha-mailbox-router`, log/database paths, old launcher flags such as `--managed-mecha-rpc`, ready/stop markers. Consumers parse these markers. Keep aliases or coordinate all scripts/test harnesses. |
| `.gitignore:49`, tooling defaults and installation instructions | Retain ignore coverage for legacy state even if a new state root is added. Never expose old databases/credentials by dropping the old ignore. |

### C. Compatibility: API, tools, runtime protocols, persisted contracts

| Surface and locations | Required migration consideration |
|---|---|
| `router/contract/managed-tools-v1.json`, `router-proxy/internal/managed/manifest.go`, `embedded_*`, root-control adapters; web `domain/workflow-states.ts`, tool permissions and timeline renderers; Android tool rendering | Tool/capability names include `mecha_context`, `mecha_git`, `mecha_mailbox_send`, `mecha_parent_message`, `mecha_pr`, `mecha_priority`, `mecha_recall`, `mecha_reminder`, `mecha_request_inline_tool_allowance`, `mecha_router`, `mecha_settled_notifications`, `mecha_state`, `mecha_verification_handoff`, plus embedded tools such as `mecha_worker`, `mecha_worker_terminate`, `mecha_send_image`, `mecha_progress`, `mecha_tool_timeout`, `mecha_restart_router`, `mecha_workflow_transition`, `mecha_blocked`, `mecha_agent_urgency`. Do not assume every token is present in the checked manifest. Audit registration and runtime-specific availability. |
| Same contracts plus workflow imports, stored workflow states, inline grants, histories, outbox/recovery | Names are authorization and interpretation inputs, not just labels. Old tool calls/history and allowed-tool lists must still resolve. Changing definition contents requires canonical hash / `definition_id` regeneration, not string replacement of opaque hashes. Test digest and manifest acceptance paths. |
| `claude_code_mcp.go`, `claude_code_runtime.go`, `embedded_subagent_runner.go` and appendix MCP matches | MCP server `mecha`, `mcp__mecha__*` prefix, Cursor `pi__mecha_*` references. Change registration, stripping, filtering, grants, tool-result handling, and clients together. Old sessions may contain these names. |
| `router/contract/manifest.json`, web `protocol/{manifest,types,guards}.ts`, `router-proxy/internal/transport/ui_live.go:21` | `mecha.web/v1` is checked by real WebSocket clients. Keep old protocol or introduce negotiated/versioned compatibility. REST paths such as `/v1/agents` and `/v1/ui-live` are not named Mecha and need no rename. |
| `router-proxy/internal/managed/{rpc,supervisor,readiness_canonical,direct_controls,child_snapshot_manager,root_control_manager,managed_goal_projection,managed_inline_allowance}.go`; `internal/store/managed_root_control_s4.go` | Versioned strings include `mecha.tools/v1`, bootstrap/policy/control/children/child-snapshot/mailbox/post-compaction/reconcile envelopes, custom types, receipt events, and hidden `/mecha-…-v1` commands. Even retired RPC paths retain persisted compatibility expectations. Inventory is not an instruction to restore retired integrations. |
| `tools/mailbox_send.py:122`, `router-proxy/internal/transport/http.go:730`, associated transport/tests | `X-Mecha-Managed-Control-Request-ID` is a live HTTP boundary. Coordinate server reads, client writes, CORS allow-headers, diagnostics, and deduplication. No browser credential or Origin authority changes. |
| `protocol/v1/schema.json:3` | `$id` is `https://mecha.local/protocol/v1/schema.json`. Decide whether to retain the schema identity or issue a new identity with compatibility. Do not assume this is a real hosted domain. |
| `internal/store/workflow_import_export.go`, `internal/transport/git_sync_http.go`; web/Android prompt templates; `.claude/skills/workflow-management/SKILL.md`, `prompt-templates.json` | Export discriminators `mecha-workflows` and `mecha-prompt-templates`, embedded instructions and permitted tools. Accept existing files and Git-synced artifacts after rename. Version or alias formats, including both import and export. |
| `tools/tmux-worker/mailbox-client.js` and associated tests | `/mecha-reply`, `MECHA_V2_FRAME`, `MECHA_V2_REPLY` are command/diagnostic contracts. Do not silently break callers or log parsers. |

### D. Environment, config, storage, deterministic identity

| Surface and locations | Required migration consideration |
|---|---|
| `tools/run_{router,webui,reflex_proxy}.sh`, `initialize.sh`, `worker_launch.py`, `worker_runtime_config.py`, desktop config; Go config/process/recovery; auth helper | Appendix includes every `MECHA_*` occurrence, including `MECHA_ROUTER_*`, `MECHA_WEBUI_*`, `MECHA_WORKER_RUNTIME_CONFIG`, `MECHA_CURSOR_MODEL`, `MECHA_PI_*`, `MECHA_ACTIVITY_DIR`, `MECHA_HOST_HOME`, `MECHA_SUPERVISOR_TOOLS`, `MECHA_KB_DIR`, `MECHA_MANAGED_*`, `MECHA_SECRET_KEY`, `MECHA_RPC_*`. Prefix allow/deny filters and child environment propagation must recognize aliases consistently. Do not accidentally revive explicitly blocked legacy variables. |
| `internal/store/managed_root_recovery*.go`, `internal/managed/config.go`, `process.go`, cold recovery, supervisor | Serialized launch recipe keys/SQL column `mecha_rpc_extension`, `MechaRPCExtension` structs, runtime generation and epoch env. Renaming internal Go symbols is different from changing SQL/JSON compatibility. Preserve old restart recipes or add migrations. |
| `tools/initialize.sh`, launcher/Makefile defaults; `embedded_kb_tools.go:23-44`; skills `kb` | State root `~/.mecha-developer/`: router SQLite, JSONL logs, managed homes/session trees, credentials, binary, caches, worker runtime config, KB, sidecar/activity directories. KB also supports `~/.mecha/kb/`. Docs mention `~/.mecha-wolfgang/` and sibling persona paths. Choose discovery order and migration ownership. Preserve SQLite WAL/SHM consistency, active session references, credential leaf permissions, retention, and rollback. Do not copy live stores opportunistically. |
| Web `src/persistence/settings-repository.ts:1-6,250-251` | Keys: `mecha.web.activeRouter`, `.routers`, `.routerAliases`, `.agentIds`, `.agentId`, `.selectedPeers`, plus old `.messages` and `.conversations` cleanup. Migrate both settings and legacy cleanup behavior. |
| Web persistence `draft-repository.ts`, `indexed-db.ts`, `managed-folder-history.ts`, `recent-agents.ts`; `domain/prompt-templates.ts`; `ConversationHeader.tsx`; `RouterLogViewer.tsx` | `mecha.web.drafts`, IndexedDB `mecha-router-web-ui`, `mecha.web.managedRecentFolders`, `mecha.web.recentAgents`, `mecha.prompt_template_usage`, `mecha.toolbar.pinnedControls[.v2]`, `mecha.web.routerLogs.v1.displayKeys`. Preserve drafts/history and existing toolbar-key migration. DB name changes need explicit cross-database migration, not only a version bump. A frontend origin change also isolates browser state independently of key names. |
| Web `App.tsx`, `AttachmentGallery.tsx`, `ConversationHeader.tsx` | DOM events `mecha:image-cached`, `mecha:pinned-controls-changed`. Coordinate all producers/listeners, storage-event handling, and mixed tabs. |
| Android `MainActivity.kt:183,3396-3397` | SharedPreferences file `mecha`, action `mecha.conversation.$conversationId`, URI `mecha://conversation/...`. Preserve preference state and pending notification intents. Inspect actual manifest registration before assuming a public deep-link promise. |
| `tools/tmux-worker/_tmux.py`, `_tmux_rest.py`, `claude_sidecar.mjs`; `internal/transport/live.go`; `cmd/mailbox-router/tmux_adapter.go` | tmux session `mecha` / substring discovery, server option `@mecha_server_id`. A rename must not detach active workers, select a wrong session, or bypass stale-server fencing. |
| `internal/crypto/secret.go:246,283` | **Keep salt `mecha-developer-mcp-v1` and fallback instance ID `default-mecha-instance` unless implementing versioned cryptographic migration.** A cosmetic replacement can make existing encrypted state unreadable. Preserve `MECHA_SECRET_KEY` compatibility without printing its value. |
| `internal/managed/claude_code_runtime.go:167`; `tools/tmux-worker/worker_launch.py:125` | Deterministic namespaces `mecha-claude-session:` and `mecha-worker:`. Replacing them changes identities for the same inputs and can break resume/reconciliation. Retain or explicitly version/map. |
| Test subprocess env and globals throughout tests/auth helper/Go test-tagged code | `MECHA_TEST_*`, acceptance barriers, crash/recovery env, `__MECHA_MOCK_*`, `__mecha_agent_hooked__`, scratch temp prefixes. Update test producer/consumer pairs only when appropriate. These are not all public product configuration. |

### E. Tests, fixtures, specifications, historical records

- Test locations: `tests/**`, `web/router-ui-react/tests/**`, `auth-helper/*.test.mjs` and preload mocks, `router-proxy/**/*_test.go`, Go test-only/tagged fixture implementations, `app/src/test/**`, `app/src/androidTest/**`, `tools/test_run_reflex_proxy.sh`, tooling audit scripts. The appendix covers all of them.
- Change expected visible branding with the product. Keep fixtures using old IDs when they prove compatibility. Add new-ID cases rather than deleting legacy evidence.
- Cover real boundaries after implementation: browser + WebSocket + REST + proxy + SQLite with deterministic LLM mocks, existing profiles, old export import, stored launch recovery, mixed old/new clients, and Android/desktop upgrade behavior. No tests were run for this docs-only inventory.
- `docs/specs/**` and `docs/specs/INDEX.md` need specification updates **before** behavior changes. New migration requirements need scenarios for conflict, failure, rollback, and retention, not only fresh installs.
- `docs/cli-archive-migration.md`, `docs/retirement-crosswalk.md`, `tools/audits/cli-archive-migration.audit.mjs`: preserve archived artifact/source identities and hash/evidence semantics. `mecha-archive` is a separate repository, not automatically `synapse-archive`.
- Root plans and validation docs contain dated `/tmp/mecha-*`, branch/worktree names, old test commands, local evidence paths, checksums, historical retired `pi-extensions/mecha-messaging` and `mecha-rpc` references. Preserve exact provenance. Update current instructions separately; do not falsify historical evidence or invent replacement artifact locations.
- Broad search includes incidental `mechanism`, `mechanical`, `schemaChanges`, `handleTimeChange`, and similar substring matches. Keep these ordinary words/symbols. Review the whole match context, not only the `mecha` substring.

### F. Artwork, binaries, generated artifacts

| Assets | Treatment |
|---|---|
| `app/src/main/res/drawable/mecha_icon.png`; `desktop/electron/assets/Mecha.icns`; `web/router-ui-react/public/{mecha-icon,mecha-watermark}.png`; mirrored `router-proxy/managed-ui/{mecha-icon,mecha-watermark}.png` | Branding candidates requiring visual inspection. Renaming a filename does not replace baked-in text, symbols, or metadata. Inspect all ICNS sizes and PNG metadata/color/alpha before replacement. `file` confirmed valid PNG/ICNS, not visual suitability. |
| `web/router-ui-react/public/sidebar-bg.jpg`, ignored `public/bg-mech.jpg`, tracked `router-proxy/managed-ui/sidebar-bg.jpg` | Inspect only if the design changes. Generic mecha-themed background is not automatically obsolete branding. The ignored source asset needs explicit handling if a future build depends on it. |
| `web/router-ui-react/avatars/*.png` (443), `alien-symbols/*.png` (64), mirrored hashed assets | Generic robots/symbols should stay unless visual review finds literal product marks. No blanket rename/removal. Compressed image pixels cannot be verified by text search. Avatar generation uses `.claude/skills/slice-avatar-grid/slice_avatar_grid.py` (compatibility entry point `tools/slice_avatar_grid.py`), defaulting to this avatar directory and `robot` filenames. Android consumes it through `app/build.gradle.kts` source sets. Preserve that producer/consumer path. |
| `router-proxy/managed-ui/**` (489 tracked files) | Shipped/generated snapshot: scanned, not excluded. Contains minified JS/CSS/HTML and mirrored assets. Regenerate from the approved web source/build path rather than hand-editing bundles. Confirm how snapshot publication is owned; this scan found no dedicated sync command to assume. Recheck embedded/served bundle and stale hashed files after regeneration. |
| All 999 binaries | Count: app icon 1, desktop icon 1, Gradle wrapper JAR 1, bundled web assets 483 plus bundled top-level images 3, web symbols 64, avatars 443, public images 3. Printable ASCII metadata was checked, with zero candidate matches. This is not OCR, archive decompression, or complete structured metadata inspection. Gradle wrapper is a dependency artifact, not a product branding target. |
| Ignored `dist`, coverage, Android `build`, packaged apps, router binary | Rebuild after source migration. Do not patch build output or dependency trees. Check packaged app metadata, executable names, and served assets at release time. |

### G. External and machine-local follow-up (not repository edits)

- Git remote currently points to `wolfgangmeyers/mecha-developer`; Go module uses `reflexivity`. External old links/import compatibility may need redirects or a staged module cutover. Do not rewrite unrelated `reflexivity` ownership.
- `mecha-wolfgang` is a sibling source of personas/skills, including `/home/wolfgang/code/mecha-wolfgang/personas/wolfgang-voice.md`. Renaming this repo does not rename that sibling. Shared home instructions, installed skill symlinks, Pi configuration/packages, editor workspaces, launch scripts, saved bookmarks, and shell aliases need a separate machine-local inventory. Existing instructions can point at retired tooling: verify existence before updating references.
- Native Pi Subagents fork at `$HOME/code/pi-packages`, Pi/Claude/Codex/Cursor integrations, MCP registrations, installed worker extensions: coordinate tool/protocol namespace changes with their owners. The deleted repository extensions are not an installation source.
- Home/root instructions describe systemd router-proxy service, Tailscale Serve, and browser `mecha.web.activeRouter`. Consider unit descriptions/paths and browser migration if the checkout moves. Network hostnames/IPs, ports, user names, and upstream machine identities do not need a branding rename. No service/unit or tailnet state was queried or changed.
- Keep `wolfgang`, `wolfgang.web`, personal names, branch-owner prefix `wolfgang/`, home account paths, `personas/wolfgang.md`, voice profile filenames, personal GitHub ownership, external providers, and citations unless a separate requirement changes those identities. They are included in the appendix as explicit keep/review candidates, not rename recommendations.
- No `.github` tree, CI workflow, git submodule, or tracked symlink exists in the scanned file set. External CI settings, releases, signing/notarization, installed applications, repository secrets, DNS, package registries, and hosted documentation were not audited. They remain release-owner checks, not assumed rename locations.

## Method, exclusions, and mechanical validation

### Discovery

Commands run from repository root:

```sh
git status --short                  # before any write
git branch --show-current
git rev-parse HEAD
git remote -v
git ls-files -z
git ls-files --others --exclude-standard -z
git status --short --ignored
git ls-files --others --ignored --exclude-standard -z
git ls-files -s                     # symlink/mode inspection
ls -la                             # root and dotfiles
find pi-extensions .claude/skills/pi-extension-maintenance .worktrees \
  -path '*/node_modules' -prune -o -path '*/__pycache__' -prune -o -type f -print
```

Source read with `git grep` and direct file inspection across each domain. Match expression, case-insensitive, no outer word boundaries:

```regex
mecha|synapse|wolfgang|reflexivity|\bmech\b
```

This includes all capitalization/separator variants of the requested names because `mecha` is a substring. `synapse` detects preexisting target names. Additional terms distinguish personal/external identities and generic artwork. Search is deliberately a superset. No claim is made that every English synonym for the product can be recognized mechanically.

Implementation used a temporary Python scanner (outside the repository). It sorted/deduplicated the NUL-delimited tracked + nonignored-untracked file lists, excluding these two inventory outputs to avoid self-reference. It scanned current bytes, recorded SHA-256 for every input, scanned paths independently, split UTF-8 text into numbered lines, and emitted one TSV row per regex match. NUL-containing files were classified as binary; printable ASCII runs of at least four bytes were checked for metadata candidates. All source text was UTF-8. It did not follow symlinks; none occurred in the input set.

To reproduce the text candidate set independently (supply the explicit file list, not a default directory search):

```sh
# Feed files in bounded batches to avoid ARG_MAX. Exclude the two inventory files.
rg --json --hidden --no-ignore -i \
  'mecha|synapse|wolfgang|reflexivity|\bmech\b' -- <explicit-text-files...>
```

Compare `path`, `line_number`, each `submatches[].match.text`, and decoded Unicode column against every TSV `kind=text` row. This independent comparison was actually run across all 1,904 text files in batches of 100. **PASS: exact multiset equality, 5,890 matches, 3,865 lines.** All 57 path records also passed an independent reconstruction/comparison against input paths. Binaries were inspected as described above. All 2,903 input SHA-256 hashes were checked after writing the appendix and remained unchanged.

### Ignored/local scope

Filesystem and ignored-path inspection found 364,655 ignored files at scan time. Dependency/build/cache counts are not occurrence counts. No ignored state contents were copied into this inventory.

| Ignored root / family | Scope decision |
|---|---|
| `.cache/`, `.config/`, `.gradle/`, `.pytest_cache/`, `go/`, `__pycache__/` | Exclude caches, local config and toolchains. Names inspected, not config contents. |
| Root/auth-helper/web `node_modules/` | Exclude dependencies, binaries, transitive names. Owned manifests/lockfiles remain included. |
| `pi-extensions/mecha-messaging/`, `pi-extensions/mecha-rpc/` | Filesystem inspection found residual dependency trees, not owned source outside `node_modules`. Record historical directory identities; do not resurrect or rename dependencies. |
| `.claude/skills/pi-extension-maintenance/` | Residual Python bytecode only, not an untracked maintained skill. |
| `.mecha-developer/` | Local logs and cache directories; exclude contents to avoid private messages, credentials, or diagnostic leakage. Path migration is covered above. |
| `.worktrees/`, empty retired MCP tool directories | No relevant owned files found. Do not infer missing archived source from empty directories. |
| `app/build/`, web `dist/`, coverage, `test-results/`, `router-proxy/router-proxy` | Exclude generated/local results; rebuild, do not source-edit. Tracked `router-proxy/managed-ui` remains fully included. |
| `web/router-ui-react/public/bg-mech.jpg` | Ignored artwork identified by filesystem path. Manual visual review only; no text/pixel data exported. |
| `.git/` | No object/history/reflog/hooks dump. HEAD, branch, tracked file metadata and remote identity only. Git history is immutable provenance, not rename scope. |

Relevant nonignored untracked files included: `docs/specs/background-bash-widget.spec.md` and `tests/e2e/background-bash-widget.spec.ts`. Other-machine installations, home state, local credentials, ignored diagnostic contents, image OCR/compressed metadata, archived repositories and remote resources are outside mechanical coverage. No native subagent tool was available in this session; scans ran locally by domain, with independent mechanical verification. No replacement agent processes were launched.

## Dirty-state handoff

Existing changes preserved, not owned by this task:

```text
 M docs/specs/INDEX.md
 M docs/specs/background-bash.spec.md
 M router-proxy/internal/deterministic/time_id.go
 M router-proxy/internal/managed/background_bash_foreground_linux.go
 M router-proxy/internal/managed/background_bash_lifecycle.go
 M router-proxy/internal/managed/background_bash_projection.go
 M router-proxy/internal/transport/http.go
 M router-proxy/internal/transport/managed_http.go
 M tests/e2e/background-bash-promotion.spec.ts
 M web/router-ui-react/src/app.css
 M web/router-ui-react/src/components/BackgroundBashActivity.tsx
 M web/router-ui-react/src/components/ConversationHeader.tsx
 M web/router-ui-react/src/transport/http-client.ts
 M web/router-ui-react/tests/component/background-bash-activity.test.tsx
?? docs/specs/background-bash-widget.spec.md
?? tests/e2e/background-bash-widget.spec.ts
```

Task additions: `docs/synapse-rename-inventory.md`, `docs/synapse-rename-occurrences.tsv`. Re-scan if source changes before implementation; the appendix describes this checkout snapshot, not a perpetual index.

### Independent validation (2026-11-11)

A separate scanner reproduced all 5,947 records from the tracked + nonignored-untracked inputs, excluding these two outputs. Header, eight-column TSV structure (literal tabs, no CSV quote interpretation), JSON contexts, scope labels, existing paths, Unicode positions and exact match/context text passed. Python and independently batched ripgrep agreed on all 5,890 text matches and 3,865 lines. All 57 path matches agreed; all 999 NUL-containing binaries had zero printable-ASCII-run matches. File/domain counts above matched. Source hashes remained stable during validation.

Direct source checks confirmed cryptographic salt/fallback identity, deterministic namespaces, npm/Android/Electron IDs, browser storage keys, manifest hash validation, HTTP header, artwork counts/types, Vite build configuration and avatar generation paths. No production tests/builds were needed or run.

The validated checkout remains at the snapshot HEAD above. Fetch found `origin/main` at `39f249c617458c9a4fa66fd0e353ed2864513662`, nine commits ahead, including changes overlapping unrelated dirty work. Those remote changes are snapshot drift, not missing matches in this checkout. This inventory does not claim to describe that newer remote snapshot. Do not force-push or disturb unrelated work to publish it.
