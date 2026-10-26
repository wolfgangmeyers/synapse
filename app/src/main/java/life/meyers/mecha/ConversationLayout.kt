package life.meyers.mecha

/** Keep the full header controls visible on wide layouts and put them behind one reachable control on phones. */
internal fun conversationHeaderUsesOverflow(widthDp: Float): Boolean = widthDp < 600f

/** The prompt manager's three primary actions need their own row in a phone-sized dialog. */
internal fun promptManagerUsesStackedActions(widthDp: Float): Boolean = widthDp < 420f

internal const val GOAL_EDITOR_MAX_HEIGHT_DP = 420f

internal data class GoalEditorLayoutState(
    val scrollable: Boolean,
    val maxHeightDp: Float,
    val usesImePadding: Boolean,
)

internal fun goalEditorLayoutState(): GoalEditorLayoutState = GoalEditorLayoutState(
    scrollable = true,
    maxHeightDp = GOAL_EDITOR_MAX_HEIGHT_DP,
    usesImePadding = true,
)

internal enum class ConversationControlAction { GOAL, MODEL, REMINDERS, PROMPT, MANAGE_PROMPTS, TERMINATE }

/** Prompt and template management remain in the mobile overflow menu at every capability state. */
internal fun conversationControlMenuActions(
    goalAvailable: Boolean,
    modelAvailable: Boolean,
    managed: Boolean = true,
): List<ConversationControlAction> = buildList {
    if (goalAvailable) add(ConversationControlAction.GOAL)
    if (modelAvailable) add(ConversationControlAction.MODEL)
    add(ConversationControlAction.REMINDERS)
    add(ConversationControlAction.PROMPT)
    add(ConversationControlAction.MANAGE_PROMPTS)
    if (managed) add(ConversationControlAction.TERMINATE)
}

/** A TalkBack activation opens or closes the same menu as a touch activation. */
internal fun toggleConversationControlMenu(expanded: Boolean): Boolean = !expanded

internal data class AgentToolbarAction(val key: String, val label: String)

internal fun agentToolbarActions(managed: Boolean, active: Boolean): List<AgentToolbarAction> = buildList {
    if (managed) {
        if (active) add(AgentToolbarAction("interrupt", "Interrupt"))
        add(AgentToolbarAction("goal", "Goal"))
        add(AgentToolbarAction("workflow", "Workflow"))
        add(AgentToolbarAction("permission", "Inline permission"))
        add(AgentToolbarAction("scratchpad", "Scratchpad"))
        add(AgentToolbarAction("monitors", "Monitors"))
        add(AgentToolbarAction("tools", "Worker tools"))
        add(AgentToolbarAction("inline-tools", "Inline tool selection"))
        add(AgentToolbarAction("children", "Children"))
        add(AgentToolbarAction("history", "Progress history"))
        add(AgentToolbarAction("manage", "Manage worker"))
    }
}

internal fun <T> filterChatTimeline(entries: List<T>, chatOnly: Boolean, isChat: (T) -> Boolean): List<T> =
    if (chatOnly) entries.filter(isChat) else entries

internal fun shouldFollowTranscript(locked: Boolean, size: Int): Boolean = !locked && size > 0
