package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SduiStateTest {

    @Test
    fun getsAndSetsValuesAcrossBlocks() {
        val state = SduiState()

        state.setValue("block_1", "action_a", "typed text")
        state.setValue("block_1", "action_b", true)
        state.setValue("block_2", "action_c", 42)

        assertEquals("typed text", state.getValue("block_1", "action_a"))
        assertEquals(true, state.getValue("block_1", "action_b"))
        assertEquals(42, state.getValue("block_2", "action_c"))
        assertNull(state.getValue("block_1", "nonexistent"))
        assertNull(state.getValue("nonexistent", "action_a"))
    }

    @Test
    fun editingFieldClearsValidationErrorForBlock() {
        val state = SduiState()
        state.setErrors(mapOf("cfg_name" to "Name is required", "cfg_temp" to "Invalid float"))

        assertEquals("Name is required", state.getError("cfg_name"))
        assertEquals("Invalid float", state.getError("cfg_temp"))

        // Edit cfg_name
        state.setValue("cfg_name", "name_input", "Agent Smith")

        // cfg_name error is cleared, cfg_temp error remains
        assertNull(state.getError("cfg_name"))
        assertEquals("Invalid float", state.getError("cfg_temp"))
    }

    @Test
    fun clearInputsRemovesValuesAndErrors() {
        val state = SduiState()
        state.setValue("composer", "msg", "draft message")
        state.setValue("other", "flag", true)
        state.setErrors(mapOf("composer" to "Too long"))

        state.clearInputs(listOf("composer"))

        assertNull(state.getValue("composer", "msg"))
        assertNull(state.getError("composer"))
        assertEquals(true, state.getValue("other", "flag"))
    }

    @Test
    fun exportsCompleteSnapshot() {
        val state = SduiState(
            mapOf(
                "b1" to mapOf("a1" to "v1", "a2" to 10),
                "b2" to mapOf("a3" to false)
            )
        )

        val snapshot = state.toSnapshot()
        assertEquals(2, snapshot.size)
        assertEquals("v1", snapshot["b1"]?.get("a1"))
        assertEquals(10, snapshot["b1"]?.get("a2"))
        assertEquals(false, snapshot["b2"]?.get("a3"))
    }
}
