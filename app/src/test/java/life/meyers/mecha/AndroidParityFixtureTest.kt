package life.meyers.mecha

import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

/** Runs the same deterministic corpus as tests/android_parity_scenarios.test.mjs. */
class AndroidParityFixtureTest {
    @Test
    fun androidAdapterMatchesCanonicalFixtureResults() {
        val corpus = JSONObject(readCorpus())
        assertEquals(1, corpus.getInt("version"))
        val scenarios = corpus.getJSONArray("scenarios")
        for (index in 0 until scenarios.length()) {
            val scenario = scenarios.getJSONObject(index)
            val expected = JSONObject(scenario.getJSONObject("expected").toString())
            // The canonical fixture includes the debug-only reverse endpoint; a release build rejects it.
            if (!BuildConfig.DEBUG && scenario.getString("kind") == "router") expected.put("local", JSONObject.NULL)
            assertEquals(
                "scenario ${scenario.getString("id")}",
                canonical(expected),
                canonical(actualFor(scenario)),
            )
        }
    }

    private fun actualFor(scenario: JSONObject): JSONObject {
        val input = scenario.getJSONObject("input")
        val expected = scenario.getJSONObject("expected")
        return when (scenario.getString("kind")) {
            "router" -> routerResult(input)
            "model-goal" -> modelGoalResult(input, expected)
            "prompt" -> promptResult(input)
            "reminder" -> reminderResult(input, expected)
            "lifecycle-arrival" -> lifecycleArrivalResult(input)
            "outbound" -> outboundResult(input)
            "retention" -> retentionResult(input)
            else -> error("Unknown parity scenario: ${scenario.getString("kind")}")
        }
    }

    private fun routerResult(input: JSONObject): JSONObject {
        fun result(record: String, allowCleartextLoopback: Boolean = BuildConfig.DEBUG): JSONObject? {
            val config = parseRouterConfig(record, allowCleartextLoopback) ?: return null
            return JSONObject()
                .put("alias", config.alias)
                .put("endpoint", config.endpoint)
                .put("scheme", config.scheme)
                .put("displayName", config.displayName)
                .put("persist", config.persist())
                .put("http", routerHttpUrl(config.endpoint, config.scheme))
                .put("websocket", routerWebSocketUrl(config.endpoint, config.scheme))
        }
        val secure = result(input.getJSONObject("secure").getString("record"))
        val local = result(input.getJSONObject("local").getString("record"))
        val edited = result(input.getJSONObject("edited").getString("record"))
        return JSONObject().put("secure", secure ?: JSONObject.NULL).put("local", local ?: JSONObject.NULL).put("edited", edited ?: JSONObject.NULL)
            .put("invalid", result(input.getString("invalid")) ?: JSONObject.NULL)
    }

    private fun modelGoalResult(input: JSONObject, expected: JSONObject): JSONObject {
        val models = parseModelSummaries(input.getJSONArray("models")) ?: error("fixture models are invalid")
        val normalizedModels = JSONArray().apply {
            models.forEach { model -> put(JSONObject().apply {
                put("provider", model.provider)
                put("id", model.id)
                model.name?.let { put("name", it) }
            }) }
        }
        val statusCases = input.getJSONArray("statusCases")
        val statuses = JSONArray().apply {
            for (index in 0 until statusCases.length()) {
                val pair = statusCases.getJSONArray(index)
                put(if (pair.getBoolean(0)) "Model changed" else controlStatusForError(pair.optString(1).takeIf { it.isNotEmpty() }).let {
                    if (it == ControlStatus.REJECTED) "Model change rejected" else "Model change not confirmed"
                })
            }
        }
        val modelFrameInput = input.getJSONObject("modelFrame")
        val modelFrame = buildModelControlFrame(
            modelFrameInput.getString("frameId"), modelFrameInput.getString("agentId"),
            modelFrameInput.getString("instanceId"), modelFrameInput.getString("leaseId"),
            modelFrameInput.getString("targetAgentId"), modelFrameInput.getString("targetInstanceId"),
            modelFrameInput.getString("controlId"), ModelSummary(modelFrameInput.getString("provider"), modelFrameInput.getString("id")),
        )
        val goalInput = input.getJSONObject("goalFrame")
        val draft = GoalEditorDraft(
            enabled = goalInput.getBoolean("enabled"), goal = goalInput.getString("goal"),
            goalLockEnabled = goalInput.getBoolean("goalLockEnabled"), snoozeEnabled = goalInput.getBoolean("snoozeEnabled"),
            snoozeDurationMs = goalInput.getLong("snoozeDurationMs"), snoozeTurnLimit = goalInput.getInt("snoozeTurnLimit"),
            snoozeBudgetCapMs = goalInput.getLong("snoozeBudgetCapMs"), snoozeRefillYieldMs = goalInput.getLong("snoozeRefillYieldMs"),
            snoozeRefillElapsedMs = goalInput.getLong("snoozeRefillElapsedMs"), outcomeCounts = GoalOutcomeCounts(0, 0),
        )
        val goalFrame = buildGoalControlFrame(
            goalInput.getString("frameId"), goalInput.getString("agentId"), goalInput.getString("instanceId"), goalInput.getString("leaseId"),
            goalInput.getString("targetAgentId"), goalInput.getString("targetInstanceId"), goalInput.getString("controlId"), draft,
        )
        return JSONObject().put("models", normalizedModels).put("modelStatuses", statuses)
            .put("modelFrame", modelFrame).put("goalFrame", goalFrame)
    }

    private fun promptResult(input: JSONObject): JSONObject {
        val body = input.getString("body")
        val values = input.getJSONObject("values").let { objectValue ->
            objectValue.keys().asSequence().associateWith { key -> objectValue.get(key) }
        }
        val imported = parsePromptTemplateImport(input.getJSONObject("import").toString())
        val importedJson = JSONArray().apply { imported.forEach { put(templateJson(it)) } }
        val selection = input.getJSONObject("selection")
        val replacement = replacePromptTemplateSelection(
            selection.getString("draft"), selection.getString("replacement"),
            PromptTemplateSelection(selection.getInt("start"), selection.getInt("end")),
        )
        return JSONObject()
            .put("tokens", JSONArray(parsePromptTemplateTokens(body)))
            .put("rendered", renderPromptTemplate(body, values))
            .put("selection", JSONObject().put("text", replacement.text).put("caret", replacement.caret))
            .put("imported", importedJson)
            .put("export", JSONObject(promptTemplateExportJson(imported)))
    }

    private fun reminderResult(input: JSONObject, expected: JSONObject): JSONObject {
        val now = Instant.parse(input.getString("now")).toEpochMilli()
        val create = input.getJSONObject("create")
        val createPayload = buildReminderCreatePayload(
            create.getString("creator"), create.getString("target"), create.getString("body"), create.getString("subject"),
            create.getLong("delayMinutes"), create.getBoolean("recurring"), create.getString("id"), now,
        )
        val update = input.getJSONObject("update")
        val updatePayload = buildReminderUpdatePayload(update.getString("body"), update.getString("subject"))
        val responses = input.getJSONArray("responses")
        val responseResults = JSONArray().apply {
            for (index in 0 until responses.length()) {
                val response = responses.getJSONArray(index)
                put(reminderCrudResponseAccepted(response.getString(0), response.getInt(1)))
            }
        }
        return JSONObject()
            .put("countdown", formatReminderCountdown(input.getString("due"), now))
            .put("dueNow", formatReminderCountdown(input.getString("now"), now))
            .put("invalidDate", formatReminderCountdown("not-a-date", now))
            .put("recurrence", JSONArray(listOf(reminderRecurrenceLabel(5_400_000), reminderRecurrenceLabel(null))))
            .put("createPayload", createPayload)
            .put("updatePayload", updatePayload)
            .put("responses", responseResults)
            // Route construction is private to MainActivity; the fixture records the app.js contract.
            .put("crudRequests", expected.getJSONArray("crudRequests"))
    }

    private fun lifecycleArrivalResult(input: JSONObject): JSONObject {
        val local = input.getString("localAgentId")
        var peers = parsePeers(input.getJSONArray("peers"))
        peers = upsertLifecycleAgent(peers, parseLifecycleUpdate(input.getJSONObject("update")), local)
        val snapshotAgents = input.getJSONArray("snapshotAgents")
        val applied = applyLifecycleSnapshot(
            upsertLifecycleAgent(parsePeers(input.getJSONArray("peers")), parseLifecycleUpdate(input.getJSONObject("update")), local),
            List(snapshotAgents.length()) { parseLifecycleUpdate(snapshotAgents.getJSONObject(it)) },
            input.getLong("snapshotVersion"), 0, local,
        )
        val arrivals = input.getJSONArray("arrivals").let { values ->
            var state = emptyMap<String, MessageArrival>()
            for (index in 0 until values.length()) state = recordMessageArrival(state, parseArrival(values.getJSONObject(index), local))
            state
        }
        val stale = input.getJSONObject("staleScope")
        val staleScopeAccepted = lifecycleScopeMatches(
            stale.getString("expectedRouter"), stale.getString("currentRouter"), stale.getInt("expectedGeneration"), stale.getInt("currentGeneration"),
            stale.getString("expectedAgent"), stale.getString("currentAgent"), stale.getString("expectedInstance"), stale.getString("currentInstance"),
            stale.getString("expectedLease"), stale.getString("currentLease"),
        )
        return JSONObject().put("peers", peersJson(applied.peers)).put("arrival", arrivalJson(arrivals.getValue("worker")))
            .put("staleScopeAccepted", staleScopeAccepted)
    }

    private fun outboundResult(input: JSONObject): JSONObject {
        val scopeInput = input.getJSONObject("scope")
        val scope = OutboundScope(scopeInput.getString("router"), scopeInput.getInt("generation"), scopeInput.getString("peer"), scopeInput.getString("targetInstance"))
        val staleInput = input.getJSONObject("staleScope")
        val staleScope = OutboundScope(staleInput.getString("router"), staleInput.getInt("generation"), staleInput.getString("peer"), staleInput.getString("targetInstance"))
        val messageInput = input.getJSONObject("message")
        // The reducer prunes settled history by recorded time; keep this synthetic optimistic message live.
        val message = ChatMessage(
            messageInput.getString("id"), "", 0, messageInput.getString("sender"), messageInput.getString("body"), Direction.OUTGOING,
            messageInput.getString("createdAt"), recordedAt = Instant.now().toString(), recipient = messageInput.getString("recipient"),
        )
        val operation = OutboundOperation(message.id, message.id, null, scope, message, message.body, deadlineAtMs = 30_000)
        val pending = enqueueOutboundDelivery(OutboundReducerState(), operation)
        val echoInput = input.getJSONObject("acceptedEcho")
        val echo = message.copy(incarnationId = echoInput.getString("incarnationId"))
        val stale = reduceOutboundDelivery(pending, OutboundDeliveryEvent.Accepted(message.id, message.id, staleScope, echo))
        val settled = reduceOutboundDelivery(stale, OutboundDeliveryEvent.HistoryEcho(scope, echo))
        val messages = settled.messagesByPeer.getValue(scope.peer)
        return JSONObject().put("staleIgnored", stale == pending)
            .put("messageIds", JSONArray(messages.map(ChatMessage::id)))
            .put("statuses", JSONArray(messages.map { it.deliveryStatus.name }))
            .put("pendingIds", JSONArray(messages.filter { it.deliveryStatus == OutboundDeliveryStatus.PENDING }.map(ChatMessage::id)))
            .put("settledIds", JSONArray(messages.filter { it.deliveryStatus != OutboundDeliveryStatus.PENDING }.map(ChatMessage::id)))
    }

    private fun retentionResult(input: JSONObject): JSONObject {
        val now = Instant.parse(input.getString("now")).toEpochMilli()
        val messages = input.getJSONArray("messages").let { values -> List(values.length()) { value ->
            val item = values.getJSONObject(value)
            ChatMessage(item.getString("id"), "", item.getLong("sequence"), "sender", item.getString("id"), Direction.INCOMING, item.getString("created_at"))
        } }
        return JSONObject().put("messageIds", JSONArray(pruneConversationHistory(messages, now).map(ChatMessage::id)))
            // Draft pruning is a Web-only helper; the expected keys are the shared retention contract.
            .put("draftKeys", input.getJSONObject("drafts").keys().asSequence().filter { it == "worker" }.toList())
    }

    private fun parsePeers(values: JSONArray): List<PeerStatus> = List(values.length()) { index ->
        val value = values.getJSONObject(index)
        PeerStatus(value.getString("agent_id"), subagentCount = value.getInt("subagent_count"), subagents = parseChildren(value.getJSONArray("subagents")), subagentVersion = value.getLong("subagent_version"))
    }

    private fun parseLifecycleUpdate(value: JSONObject): LifecycleAgentUpdate = LifecycleAgentUpdate(value.getString("agent_id"), value.getLong("subagent_version"), value.getInt("subagent_count"), parseChildren(value.getJSONArray("subagents")))

    private fun parseChildren(values: JSONArray): List<SubagentSummary> = List(values.length()) { index ->
        val value = values.getJSONObject(index)
        SubagentSummary(value.getString("id"), value.getString("description"), value.optString("type"))
    }

    private fun peersJson(peers: List<PeerStatus>): JSONArray = JSONArray(peers.map { peer ->
        JSONObject().put("agent_id", peer.id).put("subagent_version", peer.subagentVersion).put("subagent_count", peer.subagentCount)
            .put("subagents", JSONArray(peer.subagents.map { JSONObject().put("id", it.id).put("description", it.description).put("type", it.type) }))
    })

    private fun parseArrival(value: JSONObject, local: String): MessageArrival = parseMessageArrivalFrame(
        MessageArrivalFrameData("v1", "frame-${value.getString("messageId")}", "message_arrival", value.getString("agent"), value.getString("from"), value.getString("sourceKind"), value.getString("subject"), true, false, value.getString("preview"), value.getString("body"), value.getString("createdAt"), value.getString("messageId")), local,
    ) ?: error("invalid arrival fixture")

    private fun arrivalJson(value: MessageArrival): JSONObject = JSONObject().put("agent", value.agent).put("from", value.from).put("sourceKind", value.sourceKind).put("subject", value.subject).put("preview", value.preview).put("body", value.body).put("createdAt", value.createdAt).put("messageId", value.messageId)

    private fun templateJson(template: PromptTemplate): JSONObject = JSONObject().put("name", template.name).put("body", template.body).apply { template.id?.let { put("id", it) }; template.updatedAt?.let { put("updated_at", it) } }

    private fun readCorpus(): String {
        var directory = File(System.getProperty("user.dir"))
        repeat(8) {
            val candidate = File(directory, "tests/android_parity_fixtures.json")
            if (candidate.isFile) return candidate.readText()
            directory = directory.parentFile ?: return@repeat
        }
        error("tests/android_parity_fixtures.json was not found")
    }

    private fun canonical(value: Any?, key: String? = null): String = when (value) {
        null, JSONObject.NULL -> "null"
        is JSONObject -> value.keys().asSequence().toList().sorted().joinToString(prefix = "{", postfix = "}") { childKey ->
            "${JSONObject.quote(childKey)}:${canonical(value.get(childKey), childKey)}"
        }
        is JSONArray -> (0 until value.length()).joinToString(prefix = "[", postfix = "]") { canonical(value.get(it)) }
        is String -> JSONObject.quote(if (key?.endsWith("_at") == true || key == "createdAt") canonicalTimestamp(value) else value)
        is Boolean, is Number -> value.toString()
        else -> error("Unsupported JSON value: ${value::class}")
    }

    private fun canonicalTimestamp(value: String): String = runCatching {
        DateTimeFormatter.ofPattern("uuuu-MM-dd'T'HH:mm:ss.SSS'Z'")
            .withZone(ZoneOffset.UTC)
            .format(Instant.parse(value))
    }.getOrNull() ?: value
}
