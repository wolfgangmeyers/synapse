# Native operational tools review follow-up

## Independent finding ledger

| Source | Finding | Disposition |
|---|---|---|
| Supervisor message | `normalizedDetails()` returned top-level arrays directly. Router tool details must wrap them as `{ value: normalized }` while preserving top-level plain objects. | Fixed in `pi-extensions/operational-tools.js`. Added positive router-detail tests for both top-level arrays and top-level objects in `tests/operational_native_tools.test.mjs`. |

## Commands and post-fix results

| Command | Result |
|---|---|
| `node --test tests/operational_native_tools.test.mjs tests/pi_extension_standalone.test.mjs` | Passed: 45 tests, 0 failures. |
| `npm run check` | Passed. |
| `cd .claude/skills/supervisor/tools && python3 test_install_pi_extension.py` | Passed: 9 tests. The installer test was run because `operational-tools.js` is copied into the installed extension. |
| `git diff --check` | Passed with no output. |

A separate subagent recheck was unavailable because this runtime exposes no subagent launch tool. No robot-review result is claimed.
