# Phase 2 lifecycle-control entry-gate record

**Status:** all four entry gates are accepted. This is the durable record for the gates in `rpc-phase2-lifecycle-controls-spec.md` section 2. It does not authorize later lifecycle implementation by itself.

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
| G1 same-session replacement | Accepted | Entry below: `649e58007cd7a2e0e199e758dc50d78ffa66e005`. | Complete. |
| G2 five-gate readiness | Accepted | Entry below: `6aea85b7db8648b47de7f0ef9ae481399d60ee47`. | Complete. |
| G3 direct-mail termination boundary | Accepted | Entry below: `8b3a24765dd182e75b1fec63bd17e3eaeb2553b1`. | Complete. |
| G4 Pi exact-entry and deterministic append | Accepted | Entry below: `f3407591c3cadc23c9a6c550cfa156e80f9083ef` and the locked Pi proof. | Complete. |

### G1 same-session replacement — accepted 2026-09-08

- Clean replacement source commit: `649e58007cd7a2e0e199e758dc50d78ffa66e005`, tree `d72322792daa58c71473ae97927f30088d1695d9`. The prior exact-head certification records a clean target before and after validation.

  | Candidate path | SHA-256 at the accepted commit |
  | --- | --- |
  | `router-proxy/contract_manifest_test.go` | `bcbb35e4312af2bedece778bba85dea1447b2edecf00d5643dfb43b60306ac13` |
  | `router-proxy/internal/managed/process.go` | `89bd4e2e1e1a4c877df34f998ea63407ad84a44933b028bf928bace023af1655` |
  | `router-proxy/internal/managed/process_test.go` | `678fa4eae90e43ed0c3f7f385d976844cda50e028a9012b7b70a575af8166b30` |
  | `router-proxy/internal/managed/rpc.go` | `934751ca8f04ec2ee38beef1ec60ac4f197584dbc19bf5e99484183bb894ac40` |
  | `router-proxy/internal/managed/rpc_test.go` | `c3b4ccb09c84686f28bb624e2d07b47a8541b3430b2b2955999272574421c3dd` |
  | `router-proxy/internal/managed/supervisor.go` | `8c4ddef8fb19bdfa4e05f6f09d9825475662612f3334f74f0b41718452dd9c27` |
  | `router-proxy/internal/managed/supervisor_restart_execution_test.go` | `93a1d57c6234eb818fe35b540dc873e47d47369adb99d51bdcc80b471a6e4581` |
  | `router-proxy/internal/managed/supervisor_test.go` | `466605f05aee4bc24adfd942e582b221a3ef7d4cda43a658435a1befe5550f0f` |
  | `router-proxy/internal/store/managed.go` | `db55281796961b140aca6eeb4143486834b00f895e0d3241425cc4c8242562e0` |
  | `router-proxy/internal/store/managed_test.go` | `bf39750507a5e690406373d3d34cd4fae7608b636a6a7d862e3dbaaadb1f3f8d` |
  | `router-proxy/internal/store/migrations/025_managed_restart_execution.sql` | `0e97956fa73b5d455bfab533bfa66ede88600efacb1e1fc5674684f94c4bda79` |
  | `router-proxy/internal/store/store_test.go` | `945625d8214221fd4851a3ef2f8441b342b8c16fffd8ba149216dd063778ba64` |
  | `router-proxy/internal/transport/managed_http.go` | `94c9f89060b2be1d41074f0c224bdba1b71f980ed159875d6734962a55445c61` |
  | `router-proxy/internal/transport/managed_http_test.go` | `06888502c5a70f61bb2529aef0a82e5dc0c1fb56c1c9877ecbcb5b022a488038` |
  | `router-proxy/internal/transport/managed_native_test.go` | `fced266118a62cc7fc9c2aa27f41552ddba4c1b8d78b73bbd9b3b376ee7f237f` |
  | `router-proxy/internal/transport/router.go` | `ed993ae680aa8f0629b25ad005b789363389f4efad5e8dec5f10a81c845e9b66` |
  | `router/contract/manifest.json` | `933f7744ff142169bf9573651eda575f9fddc90d1e62daf45b2d4782594e616b` |

- The retained installed-Pi artifact is `/tmp/mecha-rpc-acceptance/649e58007cd7a2e0e199e758dc50d78ffa66e005/phase2-controlled-replacement/23ab4bb83bbe704e213688c30818c98cc3e37cc2e7af771493dbc534efab781e/2f0cb1e27657ec95/controlled-replacement-v1.json`, SHA-256 `cf13ca241043e61c38558015941b63f271344b3be5e70cb9a7f4e4d948330c90`. It binds the 17-path candidate hash `23ab4bb83bbe704e213688c30818c98cc3e37cc2e7af771493dbc534efab781e` to the accepted commit/tree, Pi 0.85.0, and the checked pi-subagents commit `06ed583c720ed0fe6d6cc099b26eeb384795ea39`.
- The proof command was:

  ```sh
  cd router-proxy && go test ./internal/transport -run '^TestManagedRouterWithInstalledPiCommitsReadinessSourcesAndReplacesRuntime$' -count=1 -v
  ```

  The exact-head certification records this passing installed-Pi replacement proof and the full replacement validation.
- Current-code regression target: `bdca6b10541d7a281de78d9dd919e4c9f5b89cb5`, tree `2429642fded16e99436b5cfae2edfd2637c67d30`. The current focused managed suite passed `TestRestartExecution`, settlement interruption, target readiness, close/exit arbitration, recovery, stale-binding cleanup, and failure redaction. Later direct-mail work changed shared files, so this fresh run verifies the current behavior; it does not relabel it as the accepted commit's byte-identical source.
- Acceptance decision: accepted for G1 on the current direction. This accepts the controlled same-session replacement prerequisite only.

### G2 five-gate readiness — accepted 2026-09-08

- Clean readiness source commit: `6aea85b7db8648b47de7f0ef9ae481399d60ee47`, tree `6f99b47fdad740b769d0d98df331d33545010a5d`. The prior exact-head certification records a clean target before and after validation.

  | Candidate path | SHA-256 at the accepted commit |
  | --- | --- |
  | `RPC_IMPLEMENTATION_PLAN.html` | `06e01cda1aa01125371ab3a9f350a9b60ca683340ae82b3ed8a7874a7a4e1bce` |
  | `router-proxy/internal/managed/readiness_canonical.go` | `c6248c73281b9e6c1395d55fd7a6470a562733c3ece5c09686e8a58bd55b203e` |
  | `router-proxy/internal/managed/readiness_canonical_test.go` | `05cc86f656c3f3a6e7e1560024d26246475d102a46fc789a6c746f81fe0108a3` |
  | `router-proxy/internal/managed/rpc.go` | `6605550e77eafd27b630dafe0f65f9f439c6a5784498cc09392f56cf8bdb3a5d` |
  | `router-proxy/internal/managed/rpc_test.go` | `3a7b07e53fd258827163c0f0c5f751b12621f2d33dcb6628590468d499df4d54` |
  | `router-proxy/internal/managed/supervisor.go` | `fbaae0d7cc4d9806ec74b71c62ff43a8962781d68f9956332e763914915c8d5c` |
  | `router-proxy/internal/managed/supervisor_launch_test.go` | `f14ec774e9cfa26646f64d53d90fbdf5297a8a0dbb1fe752b7525ea5126ab82d` |
  | `router-proxy/internal/managed/supervisor_test.go` | `72ab466f650f4bdc45c25872f64ffc788d7387d8712813b500676997111ba0b0` |
  | `router-proxy/internal/store/managed.go` | `3ad554ed38422256b33328318bbe4a17951136cac56345003935f3e76a21d2b4` |
  | `router-proxy/internal/store/managed_readiness_sources_test.go` | `3925e245624262d202938bf00ecce87259654bcba9803a6c2d3624e79c559a3f` |
  | `router-proxy/internal/store/managed_test.go` | `34c9304b37d1bc279fa963297acfbad4732769ad389b2acb143b4afded2ff4fd` |
  | `router-proxy/internal/store/migrations/024_managed_readiness_sources.sql` | `dafecfad2e568f8230ea758497598da89cd97ec788dc11a21ef4fd5caf0d9c55` |
  | `router-proxy/internal/store/store_test.go` | `f5529dcd22d7c9b941d846687133707c4c640e7d16447fc4def4fa6f609b12d6` |
  | `router-proxy/internal/transport/managed_http_test.go` | `8113ed729940591c19d87fd026ffa03d03cd860d6f3f58dce18969c1d286e5ce` |
  | `router-proxy/internal/transport/managed_launch_http_test.go` | `d2137d4d17e80af5c47a275b722b35314c978c13272c5db75d0c471f6ccc7577` |
  | `router-proxy/internal/transport/managed_native_test.go` | `e95fb2fe04ac3144dbfbfca3118a9ec621a85750e58f8d13c021ba344ee19db0` |
  | `tests/mecha_rpc_manifest.test.ts` | `a5fc48b57afd3b00e2e916e4b2b7ef2829063f4eb9765db3eb93179f73f6c549` |

- The retained installed-Pi artifact is `/tmp/mecha-rpc-acceptance/629c0c66301b258ad2cb372825e47a8decaf73d7/phase2-readiness-sources/89f9b669bfb594d19eb088af23741e5aa214a39feccb951a4c1620f551222bc9/c6405c36b4cb15bc/readiness-sources-v1.json`, SHA-256 `df5dcb9aa9ca689ac2ef9f66ec27555277b5621816856723ea7ad567b2a7df28`. It binds the 17-path candidate hash `89f9b669bfb594d19eb088af23741e5aa214a39feccb951a4c1620f551222bc9` to the accepted commit/tree, Pi 0.85.0, and the checked pi-subagents commit `06ed583c720ed0fe6d6cc099b26eeb384795ea39`.
- The exact-head native proof was `TestManagedRouterWithInstalledPiCommitsReadinessSources`. It records the five gates: matching session identity, exact command inventory, bootstrap acceptance receipt, exact active-tool set, and the policy plus authoritative child-snapshot source confirmations. The certification records its passing command as part of `go test ./... -count=1`.
- Current-code regression target: `bdca6b10541d7a281de78d9dd919e4c9f5b89cb5`, tree `2429642fded16e99436b5cfae2edfd2637c67d30`. The current focused managed suite passed `TestManagerMarksReadyOnlyAfterReadinessSources`, all five failure boundaries in `TestManagerSourceReadinessFailuresNeverAdmitTheInitialInstruction`, `TestPhase1ReadinessGatesRemainUnchanged`, and `TestReadinessGateRejectsExtraCommandOrActiveTool`. Later replacement and direct-mail work changed shared files, so this fresh run verifies the current behavior; it does not relabel it as the accepted commit's byte-identical source.
- Acceptance decision: accepted for G2 on the current direction. This accepts the five-gate readiness prerequisite only.

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

No migration 027, root handler, lifecycle route, P3/P4 work, or Robot Review is accepted by this record.
