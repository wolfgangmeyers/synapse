# Tracking Ticket: Remove Fragile Cryptographic Receipts and Double-Validation in Router State Machine

**Status**: Open  
**Date**: 2026-10-20  
**Owner**: Router / Core Engine  
**Type**: Architecture / Debt / Reliability  

---

## 1. Problem Summary

The router's embedded goal architecture implements redundant, multi-layered receipt verification, exact byte comparisons (`bytes.Equal`), and pseudo-cryptographic intent validation across multiple boundaries:
- In `applyEmbeddedGoalCall`: strict byte comparison and intent matching against admitted effects.
- In `embeddedGoalReceiptProducer`: scanning caller history, re-verifying tool call arguments against stored commands with serialized byte matching, and validating against live policies.
- In receipt verification and downstream state tracking (`CommitEmbeddedGoalAppliedReceipt`, `embedded_goal_applied_receipts`, fence matching, and digest assertions).

This pattern constitutes excessive overengineering with no product or systems requirement behind it. Rather than providing meaningful security or robustness in a single-user workstation runtime, this double-validation causes severe fragility:
- Harmless, benign argument variations emitted by LLMs (such as optional or extra keys like `{"action": "off", "goal": ""}`) trigger cascading multi-layer rejections (`conflicting embedded goal call`, `ErrEmbeddedGoalSDKResultUnchecked`).
- Small, reasonable boundary adjustments require patching multiple decoupled layers (admission, execution, intent verification, receipt producer, and receipt consumer) that each redundantly assert exact serialized representations.

---

## 2. Affected Components & Code Locations

1. **`router-proxy/internal/store/embedded_goal.go` (`applyEmbeddedGoalCall`)**:
   - Re-verifies admitted `embedded_effects.intent` against reconstructed arguments using exact matching and strict argument structure rules.
   - Rejection surfaces as `"conflicting embedded goal call"`.

2. **`router-proxy/internal/store/embedded_goal_receipt.go` (`embeddedGoalReceiptProducer`)**:
   - Scans historical goal calls across all bindings to prove an SDK origin.
   - Marshals and compares `call.Arguments` and `command` JSON bytes (`bytes.Equal(want, got)`).
   - Fails with `ErrEmbeddedGoalSDKResultUnchecked`, halting provider turns.

3. **`router-proxy/internal/store/embedded_goal_receipt.go` (`CommitEmbeddedGoalAppliedReceipt`)**:
   - Maintains separate `embedded_goal_applied_receipts` table with per-turn entry digest, sequence, revision, and key assertions before advancing policy projections.

4. **Associated Tests**:
   - Integration tests in `router-proxy/internal/managed/embedded_goal_continuation_test.go`, `embedded_g5_applied_receipt_red_test.go`, and related fixtures that assert on internal receipt tables rather than observable system behavior.

---

## 3. Requirements & Target Architecture

1. **Strip Cryptographic / Exact-Byte Receipt Double-Validation**:
   - Remove fragile exact-byte comparisons (`bytes.Equal(wantBytes, gotBytes)`) between tool call arguments and expected structures in the store state machine.
   - Eliminate redundant reverse-engineering of producer intent from historical effect rows in `embeddedGoalReceiptProducer`.

2. **Normalize at Boundary**:
   - Validate and normalize tool call arguments cleanly once at the admission/ingress boundary (e.g. `embeddedGoalCommand` / tool execution handler).
   - Downstream store methods should accept normalized Go domain commands without second-guessing or byte-matching raw model inputs.

3. **Simplify the Store State Machine**:
   - Simplify goal policy updates to standard transactional mutations (`UpdateManagedGoalPolicy` / atomic policy transitions) based on caller authorization and binding fences, without requiring fragile synthetic receipt records or double-checks.
   - Remove or simplify `embedded_goal_applied_receipts` if not required for external auditing, or decouple projection advancement from exact byte-level receipt matching.

4. **Tolerate Benign LLM Variations**:
   - Ensure optional, empty, or unexpected non-destructive arguments from LLM tool calls do not trigger rejection cascades across the pipeline.

---

## 4. Acceptance Criteria

- [ ] Exact serialized byte comparisons (`bytes.Equal`) on tool call payloads and receipts are eliminated from store verification logic.
- [ ] `embeddedGoalReceiptProducer` and related verification paths are stripped or replaced with direct, straightforward state transitions.
- [ ] Tool call arguments are normalized at the boundary so unexpected optional fields do not fail execution.
- [ ] All existing functional workflows (setting goals, turning goals off, status checks, continuation loops, and subagent goals) pass deterministically without receipt mismatch errors.
- [ ] Automated regression tests verify end-to-end goal transitions succeed cleanly under typical LLM payload variations.
