package life.meyers.mecha

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.util.UUID

internal const val MAX_CONVERSATION_MESSAGES = 25

internal fun retainedHistory(messages: List<ChatMessage>): List<ChatMessage> =
    messages.takeLast(MAX_CONVERSATION_MESSAGES)

internal fun boundedHistory(existing: List<ChatMessage>, message: ChatMessage): List<ChatMessage> =
    retainedHistory(existing + message)

internal object ConversationStore {
    private const val preferencesName = "mecha"
    private val lock = Any()

    fun knownPeers(context: Context, router: String): Set<String> = preferences(context)
        .getStringSet("known_peers:$router", emptySet())?.toSet() ?: emptySet()

    fun append(context: Context, router: String, peer: String, message: ChatMessage): Boolean = synchronized(lock) {
        val preferences = preferences(context)
        val existing = history(context, router, peer)
        if (message.direction == Direction.INCOMING && existing.any { it.direction == Direction.INCOMING && it.id == message.id }) return@synchronized true
        val updated = boundedHistory(existing, message)
        preferences.edit()
            .putStringSet("known_peers:$router", knownPeers(context, router) + peer)
            .putString("history:$router:$peer", encode(updated))
            .commit()
    }

    fun history(context: Context, router: String, peer: String): List<ChatMessage> {
        if (router.isBlank() || peer.isBlank()) return emptyList()
        val raw = preferences(context).getString("history:$router:$peer", null) ?: return emptyList()
        return try {
            val array = JSONArray(raw)
            val decoded = List(array.length()) { index ->
                val value = array.getJSONObject(index)
                ChatMessage(
                    id = value.optString("id", UUID.randomUUID().toString()),
                    sender = value.optString("sender", peer),
                    body = value.optString("body"),
                    direction = if (value.optString("direction") == Direction.OUTGOING.name) Direction.OUTGOING else Direction.INCOMING,
                    createdAt = value.optString("created_at", Instant.EPOCH.toString()),
                )
            }
            val retained = retainedHistory(decoded)
            if (retained.size != decoded.size) preferences(context).edit().putString("history:$router:$peer", encode(retained)).commit()
            retained
        } catch (_: Exception) {
            val migrated = retainedHistory(legacyHistory(context, peer, raw))
            preferences(context).edit().putString("history:$router:$peer", encode(migrated)).commit()
            migrated
        }
    }

    fun conversationId(context: Context, router: String, peer: String): String {
        val key = "conversation_id:$router:$peer"
        return preferences(context).getString(key, null)
            ?: UUID.randomUUID().toString().also { preferences(context).edit().putString(key, it).commit() }
    }

    private fun preferences(context: Context) = context.getSharedPreferences(preferencesName, Context.MODE_PRIVATE)

    private fun legacyHistory(context: Context, peer: String, raw: String): List<ChatMessage> = raw
        .takeUnless { it.isBlank() || it == "No messages yet" }
        ?.lines()?.filter { it.isNotBlank() }?.map { line ->
            val outgoing = line.startsWith("You · ")
            ChatMessage(
                id = UUID.randomUUID().toString(),
                sender = if (outgoing) preferences(context).getString("agent_id", "").orEmpty() else peer,
                body = line.substringAfter(" · ", line),
                direction = if (outgoing) Direction.OUTGOING else Direction.INCOMING,
                createdAt = Instant.EPOCH.toString(),
            )
        }.orEmpty()

    private fun encode(messages: List<ChatMessage>): String = JSONArray().apply {
        messages.forEach { message ->
            put(JSONObject()
                .put("id", message.id)
                .put("sender", message.sender)
                .put("body", message.body)
                .put("direction", message.direction.name)
                .put("created_at", message.createdAt))
        }
    }.toString()
}
