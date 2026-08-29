package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ConversationLayoutTest {
    @Test
    fun goalEditorKeepsControlsReachableWithScrollingAndImePadding() {
        val layout = goalEditorLayoutState()
        assertTrue(layout.scrollable)
        assertTrue(layout.usesImePadding)
        assertEquals(GOAL_EDITOR_MAX_HEIGHT_DP, layout.maxHeightDp, 0f)
        assertTrue(layout.maxHeightDp > 0f)
    }

    @Test
    fun headerUsesOverflowForNormalPhoneWidthAndInlineControlsForWideLayouts() {
        assertTrue(conversationHeaderUsesOverflow(432f))
        assertTrue(conversationHeaderUsesOverflow(599.9f))
        assertFalse(conversationHeaderUsesOverflow(600f))
        assertFalse(conversationHeaderUsesOverflow(840f))
    }

    @Test
    fun promptManagerStacksActionsInsideNarrowDialogs() {
        assertTrue(promptManagerUsesStackedActions(336f))
        assertTrue(promptManagerUsesStackedActions(419.9f))
        assertFalse(promptManagerUsesStackedActions(420f))
    }

    @Test
    fun promptAndManagementActionsRemainInTheOverflowMenu() {
        val actions = conversationControlMenuActions(goalAvailable = false, modelAvailable = false)
        assertEquals(
            listOf(ConversationControlAction.REMINDERS, ConversationControlAction.PROMPT, ConversationControlAction.MANAGE_PROMPTS),
            actions,
        )
        assertTrue(conversationControlMenuActions(goalAvailable = true, modelAvailable = true).containsAll(actions))
    }

    @Test
    fun activatingMoreControlsTogglesMenuVisibility() {
        assertTrue(toggleConversationControlMenu(false))
        assertFalse(toggleConversationControlMenu(true))
    }
}
