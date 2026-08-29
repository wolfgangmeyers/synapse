package life.meyers.mecha

import org.json.JSONArray
import org.json.JSONObject

private val LIFECYCLE_AGENT_ID = Regex("^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]*$")
private val LIFECYCLE_FRAME_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
private val LIFECYCLE_STATUSES = setOf("queued", "running", "completed", "steered", "aborted", "stopped", "error")
private const val MAX_ARRIVALS = 999
private const val MAX_ARRIVAL_FROM = 128
private const val MAX_ARRIVAL_SUBJECT = 256
private const val MAX_ARRIVAL_PREVIEW = 140
private const val MAX_ARRIVAL_BODY = 65_536

internal data class LifecycleAgentUpdate(
    val agentId: String,
    val subagentVersion: Long,
    val subagentCount: Int,
    val subagents: List<SubagentSummary>,
)

internal data class LifecycleStateFrame(
    val type: String,
    val routerVersion: Long,
    val updates: List<LifecycleAgentUpdate>,
)

internal data class LifecycleChildData(
    val id: String?,
    val description: String? = null,
    val type: String? = null,
    val status: String? = null,
)

internal data class LifecycleAgentData(
    val agentId: String?,
    val subagentVersion: Long?,
    val subagentCount: Long?,
    val subagents: List<LifecycleChildData?>?,
)

internal data class LifecycleStateFrameData(
    val version: String?,
    val frameId: String?,
    val type: String?,
    val routerVersion: Long?,
    val agents: List<LifecycleAgentData?>? = null,
    val agent: LifecycleAgentData? = null,
    val updates: List<LifecycleAgentData?>? = null,
)

internal data class MessageArrivalFrameData(
    val version: String?,
    val frameId: String? = null,
    val type: String?,
    val agentId: String? = null,
    val from: String? = null,
    val sourceKind: String? = null,
    val subject: String? = null,
    val subjectPresent: Boolean = false,
    val subjectIsNull: Boolean = false,
    val preview: String? = null,
    val body: String? = null,
    val createdAt: String? = null,
    val messageId: String? = null,
)

internal data class SubagentSyncEnableFrameData(
    val frameId: String,
    val agentId: String,
    val instanceId: String,
    val leaseId: String,
    val afterVersion: Long?,
) {
    val version: String get() = "v1"
    val type: String get() = "subagent_sync_enable"
    val wireFields: Map<String, Any?>
        get() = buildMap {
            put("version", version)
            put("frame_id", frameId)
            put("type", type)
            put("agent_id", agentId)
            put("instance_id", instanceId)
            put("lease_id", leaseId)
            if (afterVersion != null) put("after_version", afterVersion)
        }
}

internal data class MessageArrival(
    val agent: String,
    val from: String,
    val subject: String?,
    val sourceKind: String,
    val preview: String,
    val body: String,
    val createdAt: String,
    val messageId: String,
)

internal fun buildSubagentSyncEnableFrameData(
    frameId: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    afterVersion: Long? = null,
): SubagentSyncEnableFrameData {
    require(LIFECYCLE_FRAME_ID.matches(frameId))
    require(LIFECYCLE_AGENT_ID.matches(agentId))
    require(LIFECYCLE_FRAME_ID.matches(instanceId))
    require(LIFECYCLE_FRAME_ID.matches(leaseId))
    require(afterVersion == null || afterVersion >= 0)
    return SubagentSyncEnableFrameData(frameId, agentId, instanceId, leaseId, afterVersion)
}

internal fun buildSubagentSyncEnableFrame(
    frameId: String,
    agentId: String,
    instanceId: String,
    leaseId: String,
    afterVersion: Long? = null,
): JSONObject = buildSubagentSyncEnableFrameData(frameId, agentId, instanceId, leaseId, afterVersion).let { frame ->
    JSONObject().apply {
        frame.wireFields.forEach { (key, value) -> put(key, value) }
    }
}

internal fun parseLifecycleStateFrame(value: LifecycleStateFrameData): LifecycleStateFrame? {
    if (value.version != "v1" || !LIFECYCLE_FRAME_ID.matches(value.frameId.orEmpty())) return null
    val type = value.type.orEmpty()
    val routerVersion = value.routerVersion ?: return null
    if (routerVersion < 0) return null
    val updates = when (type) {
        "subagent_state_snapshot" -> parseLifecycleUpdates(value.agents)
        "subagent_state_update" -> parseLifecycleUpdate(value.agent)?.let(::listOf)
        "subagent_state_replay" -> parseLifecycleUpdates(value.updates)
        else -> null
    } ?: return null
    return LifecycleStateFrame(type, routerVersion, updates)
}

internal fun parseLifecycleStateFrame(value: JSONObject): LifecycleStateFrame? = parseLifecycleStateFrame(
    LifecycleStateFrameData(
        version = value.optString("version"),
        frameId = value.optString("frame_id"),
        type = value.optString("type"),
        routerVersion = value.safeNonNegativeLong("router_version"),
        agents = value.optJSONArray("agents")?.let { values -> List(values.length()) { index -> values.optJSONObject(index)?.toLifecycleAgentData() } },
        agent = value.optJSONObject("agent")?.toLifecycleAgentData(),
        updates = value.optJSONArray("updates")?.let { values -> List(values.length()) { index -> values.optJSONObject(index)?.toLifecycleAgentData() } },
    ),
)

private fun parseLifecycleUpdates(values: List<LifecycleAgentData?>?): List<LifecycleAgentUpdate>? {
    if (values == null || values.size > MAX_ARRIVALS) return null
    val result = ArrayList<LifecycleAgentUpdate>(values.size)
    values.forEach { value -> result += parseLifecycleUpdate(value) ?: return null }
    return result
}

private fun parseLifecycleUpdate(value: LifecycleAgentData?): LifecycleAgentUpdate? {
    if (value == null) return null
    val agent = value.agentId.orEmpty()
    val version = value.subagentVersion
    val count = value.subagentCount
    if (!LIFECYCLE_AGENT_ID.matches(agent) || version == null || count == null || version < 0 || count < 0 || count > 999) return null
    val summaries = parseLifecycleChildren(value.subagents) ?: return null
    if (count < summaries.size) return null
    return LifecycleAgentUpdate(agent, version, count.toInt(), if (count == 0L) emptyList() else summaries)
}

private fun parseLifecycleChildren(values: List<LifecycleChildData?>?): List<SubagentSummary>? {
    if (values == null || values.size > 999) return null
    val seen = mutableSetOf<String>()
    val result = ArrayList<SubagentSummary>(values.size)
    values.forEach { value ->
        if (value == null) return null
        val id = value.id?.trim()?.takeIf { it.isNotEmpty() && it.length <= 128 } ?: return null
        val description = value.description?.trim()?.takeIf { it.isNotEmpty() && it.length <= 512 } ?: ""
        val type = value.type?.trim()?.takeIf { it.isNotEmpty() && it.length <= 64 } ?: ""
        val status = value.status?.takeIf { it in LIFECYCLE_STATUSES }
        if (seen.add(id)) result += SubagentSummary(id, description.ifEmpty { id }, type.ifEmpty { status.orEmpty() })
    }
    return result
}

internal fun upsertLifecycleAgent(
    peers: List<PeerStatus>,
    incoming: LifecycleAgentUpdate,
    localAgentId: String,
): List<PeerStatus> {
    if (incoming.agentId == localAgentId) return peers
    val index = peers.indexOfFirst { it.id == incoming.agentId }
    val current = peers.getOrNull(index)
    val currentVersion = current?.subagentVersion ?: -1L
    if (incoming.subagentVersion < currentVersion) return peers
    if (incoming.subagentVersion == currentVersion && current != null
        && (current.subagentCount != incoming.subagentCount || current.subagents != incoming.subagents)) return peers
    val next = (current ?: PeerStatus(incoming.agentId)).copy(
        subagentVersion = incoming.subagentVersion,
        subagentCount = incoming.subagentCount,
        subagents = if (incoming.subagentCount == 0) emptyList() else incoming.subagents,
    )
    return if (index < 0) peers + next else peers.mapIndexed { position, peer -> if (position == index) next else peer }
}

internal data class LifecycleSnapshotResult(
    val peers: List<PeerStatus>,
    val lastSnapshotVersion: Long,
)

internal fun applyLifecycleSnapshot(
    peers: List<PeerStatus>,
    updates: List<LifecycleAgentUpdate>,
    snapshotVersion: Long,
    lastSnapshotVersion: Long = 0,
    localAgentId: String,
): LifecycleSnapshotResult {
    if (snapshotVersion < lastSnapshotVersion) return LifecycleSnapshotResult(peers, lastSnapshotVersion)
    val received = updates.associateBy { it.agentId }
    var next = peers
    received.values.forEach { update ->
        if (peers.any { it.id == update.agentId }) next = upsertLifecycleAgent(next, update, localAgentId)
    }
    next.forEach { peer ->
        if (peer.id == localAgentId || received.containsKey(peer.id)) return@forEach
        val version = peer.subagentVersion
        if (version != null && version <= snapshotVersion) {
            next = upsertLifecycleAgent(next, LifecycleAgentUpdate(peer.id, snapshotVersion, 0, emptyList()), localAgentId)
        }
    }
    return LifecycleSnapshotResult(next, snapshotVersion)
}

internal fun applyLifecycleReplay(
    peers: List<PeerStatus>,
    updates: List<LifecycleAgentUpdate>,
    routerVersion: Long,
    lastVersion: Long,
    localAgentId: String,
): Pair<List<PeerStatus>, Long> {
    if (routerVersion < lastVersion) return peers to lastVersion
    var next = peers
    // Replay entries can cover different parents and need not have contiguous versions.
    updates.forEach { next = upsertLifecycleAgent(next, it, localAgentId) }
    return next to maxOf(lastVersion, routerVersion)
}

internal fun parseMessageArrivalFrame(value: MessageArrivalFrameData, localAgentId: String): MessageArrival? {
    if (value.version != "v1" || value.type != "message_arrival") return null
    val agent = value.agentId?.takeIf { LIFECYCLE_AGENT_ID.matches(it) && it != localAgentId } ?: return null
    val from = value.from?.takeIf { it.isNotEmpty() && it.length <= MAX_ARRIVAL_FROM } ?: return null
    val kind = value.sourceKind?.takeIf { it in setOf("agent", "discord", "reminder") } ?: return null
    val preview = value.preview?.takeIf { it.length <= MAX_ARRIVAL_PREVIEW } ?: return null
    val body = value.body?.takeIf { it.length <= MAX_ARRIVAL_BODY } ?: return null
    val createdAt = value.createdAt?.takeIf { it.isNotEmpty() } ?: return null
    val messageId = value.messageId?.takeIf { LIFECYCLE_FRAME_ID.matches(it) } ?: return null
    val subject = when {
        !value.subjectPresent || value.subjectIsNull -> null
        value.subject != null -> value.subject.takeIf { it.length <= MAX_ARRIVAL_SUBJECT } ?: return null
        else -> return null
    }
    return MessageArrival(agent, from, subject, kind, preview, body, createdAt, messageId)
}

internal fun parseMessageArrivalFrame(value: JSONObject, localAgentId: String): MessageArrival? = parseMessageArrivalFrame(
    MessageArrivalFrameData(
        version = value.optString("version"),
        frameId = value.optString("frame_id").takeIf { it.isNotEmpty() },
        type = value.optString("type"),
        agentId = value.stringValue("agent_id"),
        from = value.stringValue("from"),
        sourceKind = value.optJSONObject("source")?.stringValue("kind"),
        subject = if (value.has("subject") && !value.isNull("subject")) value.stringValue("subject") else null,
        subjectPresent = value.has("subject"),
        subjectIsNull = value.has("subject") && value.isNull("subject"),
        preview = value.stringValue("preview"),
        body = value.stringValue("body"),
        createdAt = value.stringValue("created_at"),
        messageId = value.stringValue("message_id"),
    ),
    localAgentId,
)

internal fun recordMessageArrival(
    existing: Map<String, MessageArrival>,
    arrival: MessageArrival,
): Map<String, MessageArrival> {
    val next = LinkedHashMap(existing)
    next[arrival.agent] = arrival
    while (next.size > MAX_ARRIVALS) next.remove(next.keys.first())
    return next
}

internal fun reconcileMessageArrivals(
    existing: Map<String, MessageArrival>,
    incoming: Iterable<MessageArrival>,
): Map<String, MessageArrival> = incoming.fold(existing) { state, arrival -> recordMessageArrival(state, arrival) }

internal fun visibleMessageArrivals(
    arrivals: Map<String, MessageArrival>,
    filter: String,
): List<MessageArrival> {
    val normalized = filter.trim().lowercase()
    return arrivals.values.filter { arrival ->
        normalized.isBlank() || listOf(arrival.agent, arrival.from, arrival.subject.orEmpty(), arrival.preview, arrival.body, arrival.sourceKind)
            .joinToString(" ").lowercase().contains(normalized)
    }
}

internal fun lifecycleScopeMatches(
    expectedRouter: String,
    currentRouter: String,
    expectedGeneration: Int,
    currentGeneration: Int,
    expectedAgent: String,
    currentAgent: String,
    expectedInstance: String,
    currentInstance: String,
    expectedLease: String,
    currentLease: String,
): Boolean = expectedRouter == currentRouter
    && expectedGeneration == currentGeneration
    && expectedAgent == currentAgent
    && expectedInstance == currentInstance
    && expectedLease.isNotBlank()
    && expectedLease == currentLease

private fun JSONObject.toLifecycleAgentData(): LifecycleAgentData = LifecycleAgentData(
    agentId = stringValue("agent_id"),
    subagentVersion = safeNonNegativeLong("subagent_version"),
    subagentCount = safeNonNegativeLong("subagent_count"),
    subagents = optJSONArray("subagents")?.let { values -> List(values.length()) { index ->
        values.optJSONObject(index)?.let { child ->
            LifecycleChildData(
                id = child.stringValue("id"),
                description = child.stringValue("description"),
                type = child.stringValue("type"),
                status = child.stringValue("status"),
            )
        }
    } },
)

private fun JSONObject.stringValue(key: String): String? = if (has(key) && get(key) is String) getString(key) else null

private fun JSONObject.safeNonNegativeLong(key: String): Long? {
    if (!has(key)) return null
    val raw = get(key) as? Number ?: return null
    val value = raw.toDouble()
    val integer = raw.toLong()
    return integer.takeIf { value.isFinite() && value == it.toDouble() && it >= 0 }
}
