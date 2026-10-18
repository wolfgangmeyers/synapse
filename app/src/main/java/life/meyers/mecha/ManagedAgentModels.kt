package life.meyers.mecha

internal data class DirectoryListing(
    val currentPath: String,
    val parentPath: String,
    val breadcrumbs: List<Pair<String, String>>,
    val directories: List<Pair<String, String>>,
)

internal data class WorktreeCheckResult(
    val isGitRepo: Boolean,
    val isMainWorktree: Boolean,
    val hasUncommittedChanges: Boolean,
)

internal fun normalizeManagedAgentName(name: String): String = name.trim().replace(Regex("\\s+"), "-")

