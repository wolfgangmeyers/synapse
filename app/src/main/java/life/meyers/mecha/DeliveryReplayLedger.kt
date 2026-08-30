package life.meyers.mecha

import android.content.SharedPreferences
import org.json.JSONArray
import org.json.JSONObject

internal const val MAX_DELIVERY_REPLAY_LEDGER_ENTRIES = 5_000
internal const val DELIVERY_REPLAY_LEDGER_RETENTION_MS = HISTORY_RETENTION_MS * 2

internal data class DeliveryLedgerKey(
    val router: String,
    val ownerAgentId: String,
    val ownerInstanceId: String,
    val peer: String,
    val messageId: String,
    val messageIncarnationId: String,
)

internal enum class DeliveryLedgerStatus { PERSISTED, ACK_CONFIRMED }

internal data class DeliveryLedgerRecord(
    val key: DeliveryLedgerKey,
    val status: DeliveryLedgerStatus,
    val updatedAtMs: Long,
)

/**
 * Keeps delivery identity outside bounded conversation history. A replay can therefore be
 * settled without notifying again after its conversation row ages out.
 */
internal class DeliveryReplayLedger(private val preferences: SharedPreferences) {
    fun status(key: DeliveryLedgerKey, nowMs: Long = System.currentTimeMillis()): DeliveryLedgerStatus? =
        read(nowMs).firstOrNull { it.key == key }?.status

    fun markPersisted(key: DeliveryLedgerKey, nowMs: Long = System.currentTimeMillis()): Boolean {
        val records = read(nowMs).toMutableList()
        val index = records.indexOfFirst { it.key == key }
        if (index >= 0) {
            val current = records[index]
            records[index] = current.copy(
                status = if (current.status == DeliveryLedgerStatus.ACK_CONFIRMED) current.status else DeliveryLedgerStatus.PERSISTED,
                updatedAtMs = maxOf(current.updatedAtMs, nowMs),
            )
        } else {
            records += DeliveryLedgerRecord(key, DeliveryLedgerStatus.PERSISTED, nowMs)
        }
        return write(records, nowMs)
    }

    fun markAcknowledged(key: DeliveryLedgerKey, nowMs: Long = System.currentTimeMillis()): Boolean {
        val records = read(nowMs).toMutableList()
        val index = records.indexOfFirst { it.key == key }
        if (index < 0) return false
        records[index] = records[index].copy(status = DeliveryLedgerStatus.ACK_CONFIRMED, updatedAtMs = maxOf(records[index].updatedAtMs, nowMs))
        return write(records, nowMs)
    }

    private fun read(nowMs: Long): List<DeliveryLedgerRecord> = runCatching {
        val root = preferences.getString(LEDGER_PREFERENCE_KEY, null) ?: return@runCatching emptyList()
        val values = JSONArray(root)
        val records = mutableListOf<DeliveryLedgerRecord>()
        for (index in 0 until minOf(values.length(), MAX_DELIVERY_REPLAY_LEDGER_ENTRIES)) {
            val value = values.optJSONObject(index) ?: continue
            val router = value.optString("router")
            val owner = value.optString("owner_agent_id")
            val ownerInstance = value.optString("owner_instance_id")
            val peer = value.optString("peer")
            val messageId = value.optString("message_id")
            val incarnation = value.optString("message_incarnation_id")
            val status = runCatching { DeliveryLedgerStatus.valueOf(value.optString("status")) }.getOrNull()
            val updatedAt = value.optLong("updated_at", -1L)
            if (router.isBlank() || owner.isBlank() || ownerInstance.isBlank() || peer.isBlank()
                || messageId.isBlank() || incarnation.isBlank() || status == null || updatedAt < 0L
            ) continue
            if (nowMs - updatedAt <= DELIVERY_REPLAY_LEDGER_RETENTION_MS) {
                records += DeliveryLedgerRecord(
                    DeliveryLedgerKey(router, owner, ownerInstance, peer, messageId, incarnation),
                    status,
                    updatedAt,
                )
            }
        }
        records.distinctBy(DeliveryLedgerRecord::key).takeLast(MAX_DELIVERY_REPLAY_LEDGER_ENTRIES)
    }.getOrDefault(emptyList())

    private fun write(records: List<DeliveryLedgerRecord>, nowMs: Long): Boolean {
        val retained = records
            .filter { nowMs - it.updatedAtMs <= DELIVERY_REPLAY_LEDGER_RETENTION_MS }
            .distinctBy(DeliveryLedgerRecord::key)
            .takeLast(MAX_DELIVERY_REPLAY_LEDGER_ENTRIES)
        val values = JSONArray()
        retained.forEach { record ->
            values.put(JSONObject()
                .put("router", record.key.router)
                .put("owner_agent_id", record.key.ownerAgentId)
                .put("owner_instance_id", record.key.ownerInstanceId)
                .put("peer", record.key.peer)
                .put("message_id", record.key.messageId)
                .put("message_incarnation_id", record.key.messageIncarnationId)
                .put("status", record.status.name)
                .put("updated_at", record.updatedAtMs))
        }
        return preferences.edit().putString(LEDGER_PREFERENCE_KEY, values.toString()).commit()
    }

    private companion object {
        const val LEDGER_PREFERENCE_KEY = "delivery_replay_ledger"
    }
}
