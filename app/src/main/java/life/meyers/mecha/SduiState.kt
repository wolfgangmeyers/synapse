package life.meyers.mecha

import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.snapshots.SnapshotStateMap
import androidx.compose.runtime.staticCompositionLocalOf

/**
 * Ephemeral reactive state dictionary Map<String, Map<String, Any>> holding form and input
 * values along with validation errors, keyed by block_id and action_id.
 */
class SduiState(
    initialValues: Map<String, Map<String, Any>> = emptyMap()
) {
    // blockId -> (actionId -> value)
    private val valuesMap = mutableStateMapOf<String, SnapshotStateMap<String, Any>>()
    // blockId -> errorMessage
    private val errorsMap = mutableStateMapOf<String, String>()

    init {
        initialValues.forEach { (blockId, actionMap) ->
            val inner = mutableStateMapOf<String, Any>()
            inner.putAll(actionMap)
            valuesMap[blockId] = inner
        }
    }

    fun getValue(blockId: String, actionId: String): Any? {
        return valuesMap[blockId]?.get(actionId)
    }

    fun setValue(blockId: String, actionId: String, value: Any) {
        val inner = valuesMap.getOrPut(blockId) { mutableStateMapOf() }
        inner[actionId] = value
        // Clear validation error when the field is edited
        errorsMap.remove(blockId)
    }

    fun clearInputs(blockIds: List<String>) {
        blockIds.forEach { blockId ->
            valuesMap.remove(blockId)
            errorsMap.remove(blockId)
        }
    }

    fun clearAll() {
        valuesMap.clear()
        errorsMap.clear()
    }

    fun setErrors(errors: Map<String, String>) {
        errorsMap.clear()
        errorsMap.putAll(errors)
    }

    fun getError(blockId: String): String? {
        return errorsMap[blockId]
    }

    fun toSnapshot(): Map<String, Map<String, Any>> {
        val snapshot = mutableMapOf<String, Map<String, Any>>()
        valuesMap.forEach { (blockId, innerMap) ->
            snapshot[blockId] = innerMap.toMap()
        }
        return snapshot
    }
}

val LocalSduiState = staticCompositionLocalOf { SduiState() }
