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

## Gate status

| Gate | Status | Current evidence | Required acceptance evidence |
| --- | --- | --- | --- |
| G1 same-session replacement | Pending | Baseline `5d7972fd214d27b6162e51afc15b8eaef7668b97` contains the source path. | Clean commit, focused recovery artifact, and acceptance decision. |
| G2 five-gate readiness | Pending | Baseline contains `readinessGate`. | Clean commit, focused five-gate artifact, and acceptance decision. |
| G3 direct-mail termination boundary | Accepted | Entry below: `8b3a24765dd182e75b1fec63bd17e3eaeb2553b1`. | Complete. |
| G4 Pi exact-entry and deterministic append | Accepted | Entry below: `f3407591c3cadc23c9a6c550cfa156e80f9083ef` and the locked Pi proof. | Complete. |

### G3 direct-mail termination boundary — accepted 2026-09-08

- Termination-hook commit and clean source/proof target: `8b3a24765dd182e75b1fec63bd17e3eaeb2553b1`, tree `9cb16cc860972eb8af69d2b14ad832e4e1353adf`. The worktree was clean before and after the focused proofs.
- Termination-hook source paths:

  | Path | SHA-256 |
  | --- | --- |
  | `router-proxy/internal/store/delivery.go` | `b4a51d6dc8b656ea021f7a60566a26224797b67798b0f5b2048cf2840d1e870a` |
  | `router-proxy/internal/store/delivery_test.go` | `0a15ec8003d19833f040f3b42adc60a1f379d4cb28aa7e99d06870c6e9645847` |
  | `router-proxy/internal/store/managed.go` | `ffbf95b43f7510c748e7783185c758d7d84f30013d5761648a5a2068360cecdd` |

- Migration 026 is unchanged: `router-proxy/internal/store/migrations/026_managed_direct_delivery.sql` SHA-256 `531e902bea17ab9ae049852f74b242bb1701a77b0abcd5a51fb0cd449cdda2ac`.
- Focused hook proof passed:

  ```sh
  cd router-proxy && go test ./internal/store -run '^TestRetireManagedRecipientSourceForTermination' -count=1
  ```

- Current-head native direct-mail proof passed:

  ```sh
  cd router-proxy && go test ./internal/transport -run '^TestManagedDirectMailboxDelivery$' -count=1
  ```

  Artifact: `/tmp/mecha-rpc-acceptance/8b3a24765dd182e75b1fec63bd17e3eaeb2553b1/phase2-managed-direct-mail/4867cfa977901768761d30c6966f5f5e0364e31983369bb5af240b6563e79953/fc7ce68f7fd35366/managed-direct-mail-v1.json`.
  SHA-256: `25af9993d07b77ae4f4868fc0f8def57b27f04a72e62f38bfeddfb783500a97e`.
  Read-back records `result:"pass"`, the same commit/tree, one direct command, and zero managed websocket registrations, mailbox leases, and tmux receipt frames.
- Acceptance decision: accepted for G3 on the current direction. This accepts only the direct-mail termination boundary.

### G4 Pi exact-entry and deterministic append — accepted 2026-09-08

- Clean G4 source commit: `f3407591c3cadc23c9a6c550cfa156e80f9083ef`, tree `31892add5e8b7ea82872337c31caeac3851391f6`.

  | Path | SHA-256 |
  | --- | --- |
  | `pi-extensions/mecha-rpc/index.ts` | `77c3d14594858ca4043b90f1774acbdc8e79348c57e6a1a45ee6689b7cf5b9f8` |
  | `pi-extensions/mecha-rpc/rpc-client.ts` | `c21321c1a295942b556c3750455d6ace6fbf484614588f743601be0cddf0bf34` |
  | `tests/fixtures/mecha-rpc/g4-explicit-entry-id-probe.ts` | `0634a38eba60b938e840ae67089edab46128e71cec14b848664ff0d11519cd20` |
  | `tests/mecha_rpc_extension.test.ts` | `57b3b058852967479636b6a7a6d0eab9bef8adaa002f33701d939f163ab32c44` |
  | `tests/mecha_rpc_native_pi_e2e.test.ts` | `ace744c153c38ada4e040eab8a608770d232c79368720902219d07b4627e3e25` |

- The current clean proof target is `8b3a24765dd182e75b1fec63bd17e3eaeb2553b1`, tree `9cb16cc860972eb8af69d2b14ad832e4e1353adf`. The five G4 paths above are byte-identical to `f3407591c` on that target.
- Pi source repository: `https://github.com/earendil-works/pi.git`.
- Pi lock: `/private/tmp/rpc-phase2-pi-g4-source-lock.json`, SHA-256 `c736a37d07da6b8c2ff32a70d2909d8e875b5ff2be1d642cc38c5cc409c56353`.
  It locks public Pi 0.85.0 base `107d79f11072bbc8a3a757ed7fd69596bee7d68c` / tree `f5103239060686ea2983e18906857b3df54428f5`, scoped Pi commit `96a32703fbf3a3c10996b6534f79cc7ec6bfb3ef` / tree `77517d126005a8a3dcaef13b85c6512c7026bd89`, and package-lock SHA-256 `30529f16575b62c725d6fa485586a070bd06d871ea8cdc9b11cf6adc782e1564`.
  The package is `@earendil-works/pi-coding-agent` 0.85.0. Its tarball SHA-256 is `44ecf5573a601e11811eb9479c4abcfb5c69909585ccdef3188f58afa1d95cf1`; the installed executable is `/private/tmp/pi-g4-install/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js`, SHA-256 `716160c301fbbbfc40c8dc9beda58b454d697a87d03bdbfb8960ffb0a22aee1e`.
  The compatible Pi-subagents checkout is `06ed583c720ed0fe6d6cc099b26eeb384795ea39`, tree `18aa55133ecd46fc7eb9a4a3b9265e8ec247863e`.
- Focused native proof passed:

  ```sh
  MECHA_PI_BIN=/private/tmp/pi-g4-install/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js \
  MECHA_PI_PACKAGE_ROOT=/private/tmp/pi-g4-install/node_modules/@earendil-works/pi-coding-agent \
  MECHA_PI_SOURCE_LOCK=/private/tmp/rpc-phase2-pi-g4-source-lock.json \
  pi-extensions/mecha-rpc/node_modules/.bin/tsx --test \
    --test-name-pattern='^locked Pi G4 persists deterministic root results through get_entry and preserves stock RPC behavior$' \
    tests/mecha_rpc_native_pi_e2e.test.ts
  ```

  Artifact: `/tmp/mecha-rpc-acceptance/8b3a24765dd182e75b1fec63bd17e3eaeb2553b1/e93a74ef-a1d6-4081-809f-799a95c47020/pi-g4-lifecycle.json`.
  SHA-256: `a172fdda3671aba30ba8ebc5b3cc7e4477da0d7a1bd0399e64d2912dbb58c8d8`.
  Read-back records the locked source/install, `{"entry":null}` before append, then one hidden `mecha.control.result.v1` entry with deterministic ID `mcr1_123e4567e89b42d3a456426614174050`, preserved after resume. The test also proves first append acceptance, exact duplicate `already_present`, changed collision rejection, and one `message_end` for the explicit entry ID.
- Acceptance decision: accepted for G4 on the current direction. This accepts only the private exact-entry and explicit-ID append prerequisite.

G1 and G2 remain pending. No migration 027, root handler, lifecycle route, P3/P4 work, or Robot Review is accepted by this record.
