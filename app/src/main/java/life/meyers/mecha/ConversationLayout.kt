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

internal enum class ConversationControlAction { GOAL, MODEL, REMINDERS, PROMPT, MANAGE_PROMPTS }

/** Prompt and template management remain in the mobile overflow menu at every capability state. */
internal fun conversationControlMenuActions(
    goalAvailable: Boolean,
    modelAvailable: Boolean,
): List<ConversationControlAction> = buildList {
    if (goalAvailable) add(ConversationControlAction.GOAL)
    if (modelAvailable) add(ConversationControlAction.MODEL)
    add(ConversationControlAction.REMINDERS)
    add(ConversationControlAction.PROMPT)
    add(ConversationControlAction.MANAGE_PROMPTS)
}

/** A TalkBack activation opens or closes the same menu as a touch activation. */
internal fun toggleConversationControlMenu(expanded: Boolean): Boolean = !expanded
