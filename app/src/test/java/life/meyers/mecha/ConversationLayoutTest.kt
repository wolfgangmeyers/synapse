package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

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
    fun everyWidthUsesOneIconBearingOverflowMenuForAllConversationActions() {
        val layout = compactSource("ConversationLayout.kt")
        val main = compactSource("MainActivity.kt")
        val menuStart = main.indexOf("privatefunConversationHeaderOverflowMenu(")
        val screen = main.substring(main.lastIndexOf("@Composable", main.indexOf("valtranscriptState=")), menuStart)
        val callSite = callBlock(screen, screen.indexOf("ConversationHeaderOverflowMenu("))
        val menu = callSite + main.substring(menuStart, main.indexOf("@Composable", menuStart))
        val outsideMenu = screen.replace(callSite, "")

        assertFalse(layout.contains("conversationHeaderUsesOverflow") || main.contains("conversationHeaderUsesOverflow"))
        assertFalse(screen.contains("useOverflow"))
        assertFalse(main.contains("contentDescription=\"Useprompttemplate\""))
        assertEquals(1, Regex("ConversationHeaderOverflowMenu\\(").findAll(screen).count())
        for (owned in listOf("agentToolbarActions(", "chatOnly=!chatOnly", "scrollLocked=!scrollLocked", "\"Chatonly\"", "\"Lockscrolling\"")) {
            assertFalse(owned, outsideMenu.contains(owned))
        }
        for (entry in listOf("conversationControlMenuActions(capabilities.goalControlV1||managed,capabilities.modelControlV1,managed)",
            "agentToolbarActions(", ".label", "\"Chatonly\"", "\"Showactivity\"", "\"Lockscrolling\"", "\"Resumescrolling\"")) {
            assertTrue(entry, menu.contains(entry))
        }
        assertTrue("duplicate managed goal entry is skipped", Regex("""(==|!=)"goal"|"goal"(==|!=)""").containsMatchIn(menu))
        val items = Regex("DropdownMenuItem\\(").findAll(menu).count()
        assertTrue(items > 0)
        assertEquals("every menu entry has a leading icon", items, Regex("leadingIcon=").findAll(menu).count())
        for (kept in listOf("contentDescription=\"Back\"", "\"Attachimages\"", "OutlinedTextField(", "onClick=onSend", "AgentControlsDialog(")) {
            assertTrue(kept, outsideMenu.contains(kept))
        }
    }

    @Test
    fun transcriptStartsAtNewestEntryAndFollowsWithoutAnimation() {
        val main = compactSource("MainActivity.kt")
        val transcriptState = main.indexOf("state=transcriptState")
        val transcript = callBlock(main, main.lastIndexOf("LazyColumn(", transcriptState))

        assertTrue(transcript.contains("reverseLayout=true"))
        assertTrue(transcript.contains("items(timelineEntries.asReversed(),key={it.key})"))
        assertTrue(Regex("""valtranscriptState=rememberLazyListState\((0|initialFirstVisibleItemIndex=0)?\)""").containsMatchIn(main))
        assertTrue(main.contains("shouldFollowTranscript(scrollLocked,timelineEntries.size)"))
        assertTrue(main.contains("transcriptState.scrollToItem(0)"))
        assertFalse(main.contains("transcriptState.animateScrollToItem("))
        assertTrue(main.contains("filterChatTimeline(agentEvents,chatOnly)"))
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
            listOf(
                ConversationControlAction.REMINDERS,
                ConversationControlAction.PROMPT,
                ConversationControlAction.MANAGE_PROMPTS,
                ConversationControlAction.TERMINATE,
            ),
            actions,
        )
        assertTrue(conversationControlMenuActions(goalAvailable = true, modelAvailable = true).containsAll(actions))
    }

    @Test
    fun activatingMoreControlsTogglesMenuVisibility() {
        assertTrue(toggleConversationControlMenu(false))
        assertFalse(toggleConversationControlMenu(true))
    }

    private fun compactSource(name: String): String = sequenceOf(File("src/main/java/life/meyers/mecha/$name"), File("app/src/main/java/life/meyers/mecha/$name"))
        .first { it.isFile }.readText().filterNot { it.isWhitespace() }

    /** Returns one call, including its trailing lambda, from whitespace-free source. */
    private fun callBlock(source: String, start: Int): String {
        require(start >= 0)
        var depth = 0
        var index = source.indexOf('(', start)
        while (index < source.length) {
            when (source[index]) { '(', '{' -> depth++; ')', '}' -> depth-- }
            index++
            if (depth == 0 && (index == source.length || source[index] != '{')) return source.substring(start, index)
        }
        error("Unbalanced call at $start")
    }
}
