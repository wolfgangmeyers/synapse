package life.meyers.mecha

import org.json.JSONArray
import org.json.JSONObject

private val FRAME_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
private val AGENT_ID = Regex("^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]*$")

internal data class ModelSummary(
    val provider: String,
    val id: String,
    val name: String? = null,
) {
    val key: String get() = "$provider/$id"
}

internal enum class CatalogStatus { LOADING, EMPTY, READY }

internal data class ModelCatalog(
    val status: CatalogStatus,
    val models: List<ModelSummary> = emptyList(),
    val totalCount: Int = 0,
    val truncated: Boolean = false,
    val current: ModelSummary? = null,
)

internal data class GoalOutcomeCounts(
    val completed: Int,
    val notDone: Int,
)

internal data class GoalState(
    val enabled: Boolean,
    val goal: String,
    val goalLockEnabled: Boolean,
    val snoozeEnabled: Boolean,
    val snoozeDurationMs: Long,
    val snoozeTurnLimit: Int?,
    val snoozeBudgetCapMs: Long?,
    val snoozeRefillYieldMs: Long,
    val snoozeRefillElapsedMs: Long,
    val outcomeCounts: GoalOutcomeCounts,
)

internal data class GoalEditorDraft(
    val enabled: Boolean,
    val goal: String,
    val goalLockEnabled: Boolean,
    val snoozeEnabled: Boolean,
    val snoozeDurationMs: Long,
    val snoozeTurnLimit: Int?,
    val snoozeBudgetCapMs: Long?,
    val snoozeRefillYieldMs: Long,
    val snoozeRefillElapsedMs: Long,
    val outcomeCounts: GoalOutcomeCounts,
) {
    companion object {
        fun from(state: GoalState?): GoalEditorDraft = GoalEditorDraft(
            enabled = state?.enabled == true,
            goal = state?.goal.orEmpty(),
            goalLockEnabled = state?.goalLockEnabled == true,
            snoozeEnabled = state?.snoozeEnabled != false,
            snoozeDurationMs = state?.snoozeDurationMs ?: 60_000L,
            snoozeTurnLimit = state?.snoozeTurnLimit,
            snoozeBudgetCapMs = state?.snoozeBudgetCapMs,
            snoozeRefillYieldMs = state?.snoozeRefillYieldMs ?: 30_000L,
            snoozeRefillElapsedMs = state?.snoozeRefillElapsedMs ?: 60_000L,
            outcomeCounts = state?.outcomeCounts ?: GoalOutcomeCounts(0, 0),
        )
    }
}

internal enum class ControlStatus { IDLE, PENDING, ACCEPTED, REJECTED, UNCONFIRMED }

internal data class ControlState(
    val status: ControlStatus = ControlStatus.IDLE,
    val controlId: String? = null,
    val targetAgentId: String? = null,
    val targetInstanceId: String? = null,
    val errorCode: String? = null,
    val message: String? = null,
)

internal const val CAPABILITY_DISCOVERY_TIMEOUT_MS = 1_000L

internal data class ControlCapabilities(
    val modelCatalogV1: Boolean = false,
    val modelControlV1: Boolean = false,
    val goalControlV1: Boolean = false,
    val modelPendingTtlMs: Long = 30_000L,
    val subagentLifecycleV2: Boolean = false,
)

internal data class CapabilityData(
    val modelCatalogV1: Boolean = false,
    val modelPendingTtlMs: Long? = null,
    val goalControlV1: Boolean = false,
    val subagentLifecycleV2: Boolean = false,
)

internal const val CORRELATED_ACK_CONFIRMATION = "correlated_v1"

internal data class RegisteredSession(
    val requestId: String,
    val agentId: String,
    val instanceId: String,
    val leaseId: String,
    val effectiveRole: String,
    val ackConfirmation: String?,
)

internal fun usesCorrelatedAck(ackConfirmation: String?): Boolean =
    ackConfirmation == CORRELATED_ACK_CONFIRMATION

internal fun registeredSessionMatchesLease(session: RegisteredSession?, currentLeaseId: String): Boolean =
    session != null && (currentLeaseId.isBlank() || session.leaseId == currentLeaseId)

internal fun parseRegisteredSession(
    value: JSONObject,
    expectedRequestId: String,
    expectedAgentId: String,
    expectedInstanceId: String,
): RegisteredSession? {
    val allowed = setOf(
        "version", "frame_id", "type", "request_id", "agent_id", "instance_id", "lease_id",
        "expires_at", "recovered", "agent_role", "ack_confirmation", "conversation_sync",
    )
    val required = allowed - setOf("conversation_sync", "ack_confirmation")
    if (!value.keys().asSequence().all { it in allowed } || !required.all(value::has)
        || value.optString("version") != "v1" || value.optString("type") != "registered"
    ) return null
    val frameId = value.optString("frame_id")
    val requestId = value.optString("request_id")
    val agentId = value.optString("agent_id")
    val instanceId = value.optString("instance_id")
    val leaseId = value.optString("lease_id")
    val role = value.optString("agent_role")
    val ackConfirmation = when {
        !value.has("ack_confirmation") || value.isNull("ack_confirmation") -> null
        value.get("ack_confirmation") is String -> value.getString("ack_confirmation")
        else -> return null
    }
    if (!FRAME_ID.matches(frameId) || requestId != expectedRequestId || !FRAME_ID.matches(requestId)
        || agentId != expectedAgentId || !AGENT_ID.matches(agentId)
        || instanceId != expectedInstanceId || !FRAME_ID.matches(instanceId)
        || !FRAME_ID.matches(leaseId) || role != "user_interaction"
        || value.get("recovered") !is Boolean || value.optString("expires_at").isBlank()
    ) return null
    return RegisteredSession(requestId, agentId, instanceId, leaseId, role, ackConfirmation)
}

internal data class PendingControl(
    val controlId: String,
    val requestFrameId: String,
    val targetAgentId: String,
    val targetInstanceId: String,
    val router: String = "",
    val connectionGeneration: Int = 0,
    val leaseId: String = "",
)

internal data class ParsedModelCatalogFrame(
    val agentId: String,
    val instanceId: String,
    val models: List<ModelSummary>,
    val totalCount: Int,
    val truncated: Boolean,
    val current: ModelSummary?,
)

internal data class ParsedGoalStateFrame(
    val agentId: String,
    val instanceId: String,
    val state: GoalState,
)

internal data class ParsedModelControlResult(
    val agentId: String,
    val instanceId: String,
    val sourceAgentId: String,
    val controlId: String,
    val accepted: Boolean,
    val errorCode: String?,
    val currentModel: ModelSummary,
    val leaseId: String = "",
)

internal data class ParsedGoalControlResult(
    val agentId: String,
    val instanceId: String,
    val sourceAgentId: String,
    val controlId: String,
    val revision: Long,
    val accepted: Boolean,
    val errorCode: String?,
    val state: GoalState,
    val leaseId: String = "",
)

internal data class ParsedRouterError(val requestId: String?, val code: String?, val message: String?)

internal fun capabilityDiscoveryFallback(value: ControlCapabilities?): ControlCapabilities = value ?: ControlCapabilities()

internal fun parseCapabilities(value: CapabilityData): ControlCapabilities {
    // Missing, malformed, or contradictory capability fields disable controls.
    return ControlCapabilities(
        modelCatalogV1 = value.modelCatalogV1,
        modelControlV1 = value.modelCatalogV1 && value.modelPendingTtlMs != null && value.modelPendingTtlMs > 0,
        goalControlV1 = value.goalControlV1,
        modelPendingTtlMs = value.modelPendingTtlMs?.coerceIn(1L, 300_000L) ?: 30_000L,
        subagentLifecycleV2 = value.subagentLifecycleV2,
    )
}

internal fun parseCapabilities(value: JSONObject): ControlCapabilities {
    val modelLimits = value.optJSONObject("model_control_limits")
    return parseCapabilities(CapabilityData(
        modelCatalogV1 = value.optBoolean("model_catalog_v1", false),
        modelPendingTtlMs = modelLimits?.safeLong("pending_ttl_ms"),
        goalControlV1 = value.has("goal_control_v1") && value.optBoolean("goal_control_v1", false),
        subagentLifecycleV2 = value.has("subagent_lifecycle_v2") && value.optBoolean("subagent_lifecycle_v2", false),
    ))
}

internal fun parseModelSummary(value: JSONObject?): ModelSummary? {
    if (value == null || !value.keys().asSequence().all { it in setOf("provider", "id", "name") }) return null
    val provider = value.optString("provider").trim()
    val id = value.optString("id").trim()
    val name = if (value.has("name")) value.optString("name").trim() else ""
    if (provider.isEmpty() || provider.length > 128 || id.isEmpty() || id.length > 256 || name.length > 256) return null
    return ModelSummary(provider, id, name.takeIf { it.isNotEmpty() })
}

internal fun parseModelSummaries(value: JSONArray?): List<ModelSummary>? {
    if (value == null || value.length() > 1_000) return null
    val seen = mutableSetOf<String>()
    val result = mutableListOf<ModelSummary>()
    for (index in 0 until value.length()) {
        val model = parseModelSummary(value.optJSONObject(index)) ?: return null
        if (seen.add(model.key)) result += model
    }
    return result
}

internal fun parseModelCatalogFrame(value: JSONObject): ParsedModelCatalogFrame? {
    if (!hasExactKeys(value, setOf("version", "frame_id", "type", "agent_id", "instance_id", "model_summaries", "total_count", "truncated", "current_model", "generated_at"), setOf("version", "frame_id", "type", "agent_id", "instance_id", "model_summaries", "generated_at"))) return null
    if (value.optString("version") != "v1" || value.optString("type") != "agent_model_catalog") return null
    val agent = value.optString("agent_id")
    val instance = value.optString("instance_id")
    if (!AGENT_ID.matches(agent) || !FRAME_ID.matches(instance) || !FRAME_ID.matches(value.optString("frame_id"))) return null
    val models = parseModelSummaries(value.optJSONArray("model_summaries")) ?: return null
    val total = if (value.has("total_count")) value.safeInt("total_count") else models.size
    if (total == null || total < models.size || total > 1_000_000) return null
    val truncated = value.optBoolean("truncated", false)
    val current = if (value.has("current_model")) parseModelSummary(value.optJSONObject("current_model")) else null
    if (value.has("current_model") && current == null) return null
    return ParsedModelCatalogFrame(agent, instance, models, total, truncated || total > models.size, current)
}

internal fun parseGoalStateFrame(value: JSONObject): ParsedGoalStateFrame? {
    if (!hasExactKeys(value, setOf("version", "frame_id", "type", "agent_id", "instance_id", "goal_state", "generated_at"), setOf("version", "frame_id", "type", "agent_id", "instance_id", "goal_state", "generated_at"))) return null
    if (value.optString("version") != "v1" || value.optString("type") != "agent_goal_state") return null
    val agent = value.optString("agent_id")
    val instance = value.optString("instance_id")
    if (!AGENT_ID.matches(agent) || !FRAME_ID.matches(instance) || !FRAME_ID.matches(value.optString("frame_id"))) return null
    return parseGoalStateValue(value.optJSONObject("goal_state"))?.let { ParsedGoalStateFrame(agent, instance, it) }
}

internal fun parseModelControlResult(value: JSONObject): ParsedModelControlResult? {
    val required = setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "accepted", "error_code", "current_model")
    if (!hasExactKeys(value, required, required - setOf("error_code", "current_model"))) return null
    if (value.optString("version") != "v1" || value.optString("type") != "model_control_result") return null
    val agent = value.optString("agent_id")
    val instance = value.optString("instance_id")
    val source = value.optString("source_agent_id")
    val control = value.optString("control_id")
    val accepted = if (!value.has("accepted") || value.get("accepted") !is Boolean) return null else value.getBoolean("accepted")
    val current = parseModelSummary(value.optJSONObject("current_model")) ?: return null
    val error = if (value.has("error_code")) value.optString("error_code").takeIf { it.isNotEmpty() } ?: return null else null
    if (!AGENT_ID.matches(agent) || !AGENT_ID.matches(source) || !FRAME_ID.matches(instance) || !FRAME_ID.matches(control) || !FRAME_ID.matches(value.optString("frame_id")) || accepted && error != null || !accepted && error == null) return null
    val lease = value.optString("lease_id")
    if (!FRAME_ID.matches(lease)) return null
    return ParsedModelControlResult(agent, instance, source, control, accepted, error, current, lease)
}

internal fun parseGoalControlResult(value: JSONObject): ParsedGoalControlResult? {
    val required = setOf("version", "frame_id", "type", "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "revision", "accepted", "error_code", "goal_state")
    if (!hasExactKeys(value, required, required - setOf("error_code"))) return null
    if (value.optString("version") != "v1" || value.optString("type") != "goal_control_result") return null
    val agent = value.optString("agent_id")
    val instance = value.optString("instance_id")
    val source = value.optString("source_agent_id")
    val control = value.optString("control_id")
    val revision = value.safeLong("revision")
    val accepted = if (!value.has("accepted") || value.get("accepted") !is Boolean) return null else value.getBoolean("accepted")
    val error = if (value.has("error_code")) value.optString("error_code").takeIf { it.isNotEmpty() } ?: return null else null
    val state = parseGoalStateValue(value.optJSONObject("goal_state")) ?: return null
    if (revision == null || revision < 1 || !AGENT_ID.matches(agent) || !AGENT_ID.matches(source) || !FRAME_ID.matches(instance) || !FRAME_ID.matches(control) || !FRAME_ID.matches(value.optString("frame_id")) || accepted && error != null || !accepted && error == null) return null
    val lease = value.optString("lease_id")
    if (!FRAME_ID.matches(lease)) return null
    return ParsedGoalControlResult(agent, instance, source, control, revision, accepted, error, state, lease)
}

internal fun parseRouterError(value: JSONObject): ParsedRouterError? {
    val allowed = setOf("version", "frame_id", "type", "request_id", "code", "message")
    if (!value.keys().asSequence().all { it in allowed } || value.optString("type") != "error") return null
    val request = value.optString("request_id").takeIf { it.isNotEmpty() }
    val code = value.optString("code").takeIf { it.isNotEmpty() }
    if (value.has("request_id") && request == null) return null
    if (value.has("code") && code == null) return null
    return ParsedRouterError(request, code, value.optString("message").takeIf { it.isNotEmpty() })
}

internal fun buildModelControlFrame(
    frameId: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    targetAgentId: String,
    targetInstanceId: String,
    controlId: String,
    model: ModelSummary,
): JSONObject {
    require(FRAME_ID.matches(frameId) && AGENT_ID.matches(agentId) && FRAME_ID.matches(instanceId) && FRAME_ID.matches(leaseId))
    require(AGENT_ID.matches(targetAgentId) && FRAME_ID.matches(targetInstanceId) && FRAME_ID.matches(controlId))
    require(model.provider.length in 1..128 && model.id.length in 1..256)
    return JSONObject()
        .put("version", "v1").put("frame_id", frameId).put("type", "model_control")
        .put("agent_id", agentId).put("instance_id", instanceId).put("lease_id", leaseId)
        .put("target_agent_id", targetAgentId).put("target_instance_id", targetInstanceId)
        .put("control_id", controlId).put("action", "set_model")
        .put("model", JSONObject().put("provider", model.provider).put("id", model.id))
}

internal fun buildGoalControlFrame(
    frameId: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    targetAgentId: String,
    targetInstanceId: String,
    controlId: String,
    draft: GoalEditorDraft,
): JSONObject {
    require(FRAME_ID.matches(frameId) && AGENT_ID.matches(agentId) && FRAME_ID.matches(instanceId) && FRAME_ID.matches(leaseId))
    require(AGENT_ID.matches(targetAgentId) && FRAME_ID.matches(targetInstanceId) && FRAME_ID.matches(controlId))
    val action = if (draft.enabled) "set" else "off"
    require(validateGoalDraft(draft) == null)
    return JSONObject()
        .put("version", "v1").put("frame_id", frameId).put("type", "goal_control")
        .put("agent_id", agentId).put("instance_id", instanceId).put("lease_id", leaseId)
        .put("target_agent_id", targetAgentId).put("target_instance_id", targetInstanceId)
        .put("control_id", controlId).put("action", action).put("goal", if (draft.enabled) draft.goal else "")
        .put("settings", JSONObject()
            .put("goal_lock_enabled", draft.goalLockEnabled)
            .put("snooze_enabled", draft.snoozeEnabled)
            .put("snooze_duration_ms", draft.snoozeDurationMs)
            .put("snooze_turn_limit", draft.snoozeTurnLimit ?: JSONObject.NULL)
            .put("snooze_budget_cap_ms", draft.snoozeBudgetCapMs ?: JSONObject.NULL)
            .put("snooze_refill_yield_ms", draft.snoozeRefillYieldMs)
            .put("snooze_refill_elapsed_ms", draft.snoozeRefillElapsedMs))
}

internal fun validateGoalDraft(draft: GoalEditorDraft): String? = when {
    draft.goal.length > 4_000 -> "invalid_goal"
    draft.enabled && draft.goal.trim().isEmpty() -> "invalid_goal"
    draft.snoozeDurationMs < 1 || draft.snoozeRefillYieldMs < 1 || draft.snoozeRefillElapsedMs < 1 -> "invalid_settings"
    draft.snoozeTurnLimit != null && draft.snoozeTurnLimit !in 1..20 -> "invalid_settings"
    draft.snoozeBudgetCapMs != null && draft.snoozeBudgetCapMs < 1 -> "invalid_settings"
    else -> null
}

internal fun matchesPending(pending: PendingControl, targetAgentId: String, targetInstanceId: String, controlId: String, sourceAgentId: String): Boolean =
    pending.controlId == controlId && pending.targetAgentId == targetAgentId && pending.targetInstanceId == targetInstanceId && pending.targetAgentId != sourceAgentId

internal fun controlStatusForError(code: String?): ControlStatus =
    if (code == "model_control_expired" || code == "goal_control_expired" || code == "router_disconnected" || code == "router_reconnecting") ControlStatus.UNCONFIRMED else ControlStatus.REJECTED

internal fun parseGoalStateValue(value: JSONObject?): GoalState? {
    val allowed = setOf("enabled", "goal", "goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms", "outcome_counts")
    if (value == null || !value.keys().asSequence().all { it in allowed } || allowed.any { !value.has(it) }) return null
    val enabled = if (value.get("enabled") !is Boolean) return null else value.getBoolean("enabled")
    val goal = value.optString("goal")
    val lock = if (value.get("goal_lock_enabled") !is Boolean) return null else value.getBoolean("goal_lock_enabled")
    val snooze = if (value.get("snooze_enabled") !is Boolean) return null else value.getBoolean("snooze_enabled")
    val duration = value.safeLong("snooze_duration_ms") ?: return null
    val turnsResult = value.nullableLong("snooze_turn_limit")
    val budgetResult = value.nullableLong("snooze_budget_cap_ms")
    if (!turnsResult.valid || !budgetResult.valid) return null
    val turns = turnsResult.value
    val budget = budgetResult.value
    val refillYield = value.safeLong("snooze_refill_yield_ms") ?: return null
    val refillElapsed = value.safeLong("snooze_refill_elapsed_ms") ?: return null
    val counts = value.optJSONObject("outcome_counts") ?: return null
    if (!counts.keys().asSequence().all { it == "COMPLETED" || it == "NOT_DONE" } || !counts.has("COMPLETED") || !counts.has("NOT_DONE")) return null
    val completed = counts.safeInt("COMPLETED") ?: return null
    val notDone = counts.safeInt("NOT_DONE") ?: return null
    if (goal.length > 4_000 || enabled && goal.isEmpty() || !enabled && goal.isNotEmpty() || duration < 1 || refillYield < 1 || refillElapsed < 1 || turns != null && turns !in 1..20 || budget != null && budget < 1 || completed < 0 || notDone < 0) return null
    return GoalState(enabled, if (enabled) goal else "", lock, snooze, duration, turns?.toInt(), budget, refillYield, refillElapsed, GoalOutcomeCounts(completed, notDone))
}

private fun hasExactKeys(value: JSONObject, allowed: Set<String>, required: Set<String>): Boolean =
    value.keys().asSequence().all { it in allowed } && required.all(value::has)

private fun JSONObject.safeLong(key: String): Long? {
    val raw = if (has(key)) get(key) else return null
    val number = raw as? Number ?: return null
    val value = number.toDouble()
    return number.toLong().takeIf { value.isFinite() && value == it.toDouble() && it > 0 }
}

private data class NullableLongResult(val valid: Boolean, val value: Long?)

private fun JSONObject.nullableLong(key: String): NullableLongResult {
    if (!has(key) || isNull(key)) return NullableLongResult(has(key), null)
    val value = safeLong(key) ?: return NullableLongResult(false, null)
    return NullableLongResult(true, value)
}

private fun JSONObject.safeInt(key: String): Int? = safeNonNegativeLong(key)?.takeIf { it <= Int.MAX_VALUE }?.toInt()

private fun JSONObject.safeNonNegativeLong(key: String): Long? {
    val raw = if (has(key)) get(key) else return null
    val number = raw as? Number ?: return null
    val value = number.toDouble()
    return number.toLong().takeIf { value.isFinite() && value == it.toDouble() && it >= 0 }
}
