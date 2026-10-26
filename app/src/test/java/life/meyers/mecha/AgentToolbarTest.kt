package life.meyers.mecha

import org.junit.Assert.*
import org.junit.Test

class AgentToolbarTest {
    @Test fun managedActionsAreReachableAndLegacyNeverTargetsManagedApi() {
        assertTrue(agentToolbarActions(false, true).isEmpty())
        val actions = agentToolbarActions(true, false).map { it.key }
        assertEquals(listOf("goal", "workflow", "permission", "scratchpad", "monitors", "tools", "inline-tools", "children", "history", "manage"), actions)
        assertFalse(actions.contains("interrupt"))
        assertEquals(listOf("interrupt") + actions, agentToolbarActions(true, true).map { it.key })
        assertFalse(conversationControlMenuActions(false, false, false).contains(ConversationControlAction.TERMINATE))
        assertTrue(conversationControlMenuActions(false, false, true).contains(ConversationControlAction.TERMINATE))
    }
}
