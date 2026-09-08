# Phase 2 lifecycle-control entry-gate record

**Status:** pending. This is the durable record for the gates in `rpc-phase2-lifecycle-controls-spec.md` section 2. It does not accept a gate by itself.

## Record schema

An accepted gate gets one dated entry below. Each entry must name:

- gate ID and status;
- the full clean source commit and tree;
- the changed source paths and their SHA-256 values;
- the focused validation artifact path and SHA-256;
- the command or native proof that produced the artifact; and
- the reviewer or acceptance decision.

The Pi gate also names its source repository and commit, package version, built executable path and SHA-256, and the exact `get_entry` response shapes: `{"entry":null}` or one bounded entry. The direct-mail gate also names the unchanged 026 migration checksum, the termination-hook commit, and the direct-mail test artifact. The 027 gate names the migration checksum and upgrade/rollback artifact.

## Pending gates

| Gate | Status | Current evidence | Required acceptance evidence |
| --- | --- | --- | --- |
| G1 same-session replacement | Pending | Baseline `5d7972fd214d27b6162e51afc15b8eaef7668b97` contains the source path. | Clean commit, focused recovery artifact, and acceptance decision. |
| G2 five-gate readiness | Pending | Baseline contains `readinessGate`. | Clean commit, focused five-gate artifact, and acceptance decision. |
| G3 direct-mail termination boundary | Pending | Migration 026 checksum is `531e902bea17ab9ae049852f74b242bb1701a77b0abcd5a51fb0cd449cdda2ac`. The required termination hook is not yet present. | A post-026 clean commit containing `RetireManagedRecipientSourceForTermination`, its direct-mail-only tests, and the required artifact. |
| G4 Pi exact-entry and deterministic append | Pending | The inspected Pi revision has `SessionManager.getEntry`, but no typed `get_entry` RPC or validated explicit entry-ID append seam. | Tested Pi source and install record with both seams, their focused tests, binary hash, and native smoke artifact. |

No migration 027, root handler, or lifecycle route is accepted while any gate remains pending.
