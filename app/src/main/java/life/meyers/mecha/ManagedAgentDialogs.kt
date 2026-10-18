package life.meyers.mecha

import android.os.Handler
import android.os.Looper
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import kotlinx.coroutines.delay

private fun runOnMainThread(action: () -> Unit) {
    if (Looper.myLooper() == Looper.getMainLooper()) {
        action()
    } else {
        Handler(Looper.getMainLooper()).post(action)
    }
}

@Composable
internal fun LaunchWorkerDialog(
    client: ManagedAgentClient,
    onDismiss: () -> Unit,
    onLaunched: (agentName: String) -> Unit,
) {
    var name by remember { mutableStateOf("") }
    var cwd by remember { mutableStateOf("") }
    var instruction by remember { mutableStateOf("") }

    var selectedModel by remember { mutableStateOf<ModelSummary?>(null) }
    var defaultModel by remember { mutableStateOf<ModelSummary?>(null) }
    var availableModels by remember { mutableStateOf<List<ModelSummary>>(emptyList()) }
    var modelsLoading by remember { mutableStateOf(false) }

    var isMainWorktree by remember { mutableStateOf(false) }
    var worktreeEnabled by remember { mutableStateOf(false) }
    var worktreeBranch by remember { mutableStateOf("") }
    var worktreeBranchUserEdited by remember { mutableStateOf(false) }

    var directoryPickerOpen by remember { mutableStateOf(false) }
    var launchPending by remember { mutableStateOf(false) }
    var launchError by remember { mutableStateOf<String?>(null) }

    // Fetch models on open
    LaunchedEffect(Unit) {
        modelsLoading = true
        client.fetchModels { defModel, models ->
            runOnMainThread {
                defaultModel = defModel
                availableModels = models
                modelsLoading = false
            }
        }
    }

    // Debounce cwd worktree check
    LaunchedEffect(cwd) {
        val trimmed = cwd.trim()
        if (trimmed.isBlank()) {
            isMainWorktree = false
            worktreeEnabled = false
            return@LaunchedEffect
        }
        delay(400)
        client.checkWorktree(trimmed) { isGit, isMain, _ ->
            runOnMainThread {
                isMainWorktree = isGit && isMain
                if (!isMainWorktree) {
                    worktreeEnabled = false
                }
            }
        }
    }

    // Auto-slugify branch name from agent name when not user-edited
    LaunchedEffect(name, worktreeBranchUserEdited) {
        if (!worktreeBranchUserEdited) {
            worktreeBranch = normalizeManagedAgentName(name).lowercase()
        }
    }

    val canLaunch = name.isNotBlank() && cwd.isNotBlank() && !launchPending &&
            (!worktreeEnabled || worktreeBranch.isNotBlank())

    AlertDialog(
        onDismissRequest = { if (!launchPending) onDismiss() },
        containerColor = Color(0xFF151925),
        titleContentColor = Color(0xFFF2F4FF),
        textContentColor = Color(0xFFF2F4FF),
        title = {
            Text("Launch Worker", fontWeight = FontWeight.Bold)
        },
        text = {
            LazyColumn(
                modifier = Modifier
                    .fillMaxWidth()
                    .heightIn(max = 480.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp)
            ) {
                item {
                    OutlinedTextField(
                        value = name,
                        onValueChange = { name = it },
                        modifier = Modifier.fillMaxWidth(),
                        singleLine = true,
                        label = { Text("Worker name") },
                        placeholder = { Text("e.g. feature-worker") },
                        enabled = !launchPending,
                    )
                }

                item {
                    Column(Modifier.fillMaxWidth()) {
                        Row(
                            Modifier.fillMaxWidth(),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            OutlinedTextField(
                                value = cwd,
                                onValueChange = { cwd = it },
                                modifier = Modifier.weight(1f),
                                singleLine = true,
                                label = { Text("Working directory (CWD)") },
                                placeholder = { Text("/path/to/repo") },
                                enabled = !launchPending,
                            )
                            Spacer(Modifier.width(8.dp))
                            FilledTonalButton(
                                onClick = { directoryPickerOpen = true },
                                enabled = !launchPending,
                                modifier = Modifier.height(56.dp).padding(top = 8.dp),
                                colors = ButtonDefaults.filledTonalButtonColors(
                                    containerColor = Color(0xFF202638),
                                    contentColor = Color(0xFF9DA8FF),
                                ),
                                shape = RoundedCornerShape(12.dp),
                            ) {
                                Icon(Icons.Default.Folder, contentDescription = "Browse directory")
                            }
                        }
                    }
                }

                if (isMainWorktree) {
                    item {
                        Card(
                            colors = CardDefaults.cardColors(containerColor = Color(0xFF202638)),
                            shape = RoundedCornerShape(12.dp),
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                                Row(
                                    Modifier.fillMaxWidth(),
                                    verticalAlignment = Alignment.CenterVertically
                                ) {
                                    Column(Modifier.weight(1f)) {
                                        Text(
                                            "Create a worktree",
                                            style = MaterialTheme.typography.bodyMedium,
                                            fontWeight = FontWeight.SemiBold,
                                            color = Color(0xFFF2F4FF)
                                        )
                                        Text(
                                            "Isolate worker changes in a separate git branch",
                                            style = MaterialTheme.typography.bodySmall,
                                            color = Color(0xFFB5BCD0)
                                        )
                                    }
                                    Switch(
                                        checked = worktreeEnabled,
                                        onCheckedChange = { worktreeEnabled = it },
                                        enabled = !launchPending,
                                    )
                                }
                                if (worktreeEnabled) {
                                    OutlinedTextField(
                                        value = worktreeBranch,
                                        onValueChange = {
                                            worktreeBranchUserEdited = true
                                            worktreeBranch = it
                                        },
                                        modifier = Modifier.fillMaxWidth(),
                                        singleLine = true,
                                        label = { Text("Branch name") },
                                        placeholder = { Text("feature-branch") },
                                        enabled = !launchPending,
                                    )
                                }
                            }
                        }
                    }
                }

                item {
                    var modelMenuExpanded by remember { mutableStateOf(false) }
                    val currentDisplay = (selectedModel?.name?.takeIf { it.isNotBlank() } ?: selectedModel?.id)
                        ?: defaultModel?.let { "${it.name?.takeIf { n -> n.isNotBlank() } ?: it.id} (Router default)" }
                        ?: "Router default"

                    Column(Modifier.fillMaxWidth()) {
                        Text(
                            "Model",
                            style = MaterialTheme.typography.bodySmall,
                            color = Color(0xFFB5BCD0),
                            modifier = Modifier.padding(bottom = 4.dp)
                        )
                        Box(Modifier.fillMaxWidth()) {
                            OutlinedCard(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .clickable(enabled = !launchPending) { modelMenuExpanded = true },
                                shape = RoundedCornerShape(12.dp),
                            ) {
                                Row(
                                    Modifier
                                        .fillMaxWidth()
                                        .padding(horizontal = 16.dp, vertical = 14.dp),
                                    verticalAlignment = Alignment.CenterVertically
                                ) {
                                    Text(
                                        text = currentDisplay,
                                        modifier = Modifier.weight(1f),
                                        style = MaterialTheme.typography.bodyMedium,
                                        color = Color(0xFFF2F4FF),
                                        maxLines = 1,
                                        overflow = TextOverflow.Ellipsis
                                    )
                                    Text("▾", color = Color(0xFFB5BCD0))
                                }
                            }
                            DropdownMenu(
                                expanded = modelMenuExpanded,
                                onDismissRequest = { modelMenuExpanded = false },
                                containerColor = Color(0xFF151925),
                            ) {
                                DropdownMenuItem(
                                    text = {
                                        Text(
                                            defaultModel?.let { "${it.name?.takeIf { n -> n.isNotBlank() } ?: it.id} (Router default)" } ?: "Router default",
                                            color = Color(0xFFF2F4FF)
                                        )
                                    },
                                    onClick = {
                                        selectedModel = null
                                        modelMenuExpanded = false
                                    }
                                )
                                availableModels.forEach { model ->
                                    DropdownMenuItem(
                                        text = {
                                            Text(
                                                model.name?.takeIf { it.isNotBlank() } ?: model.id,
                                                color = Color(0xFFF2F4FF)
                                            )
                                        },
                                        onClick = {
                                            selectedModel = model
                                            modelMenuExpanded = false
                                        }
                                    )
                                }
                            }
                        }
                    }
                }

                item {
                    OutlinedTextField(
                        value = instruction,
                        onValueChange = { instruction = it },
                        modifier = Modifier.fillMaxWidth(),
                        minLines = 2,
                        maxLines = 5,
                        label = { Text("Initial instruction (optional)") },
                        placeholder = { Text("What should the worker work on?") },
                        enabled = !launchPending,
                    )
                }

                if (launchError != null) {
                    item {
                        Text(
                            launchError ?: "",
                            style = MaterialTheme.typography.bodySmall,
                            color = Color(0xFFFF8A80),
                        )
                    }
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    launchPending = true
                    launchError = null
                    val normalizedName = normalizeManagedAgentName(name)
                    client.launchManagedAgent(
                        name = normalizedName,
                        cwd = cwd.trim(),
                        model = selectedModel,
                        instruction = instruction.trim(),
                        worktree = worktreeEnabled,
                        worktreeBranch = worktreeBranch.trim(),
                    ) { success, error ->
                        runOnMainThread {
                            launchPending = false
                            if (success) {
                                onLaunched(normalizedName)
                            } else {
                                launchError = error ?: "Failed to launch worker"
                            }
                        }
                    }
                },
                enabled = canLaunch,
                colors = ButtonDefaults.buttonColors(
                    containerColor = Color(0xFF9DA8FF),
                    contentColor = Color(0xFF0B0D14)
                ),
                shape = RoundedCornerShape(12.dp),
            ) {
                Text(if (launchPending) "Launching…" else "Launch", fontWeight = FontWeight.Bold)
            }
        },
        dismissButton = {
            TextButton(
                onClick = onDismiss,
                enabled = !launchPending,
            ) {
                Text("Cancel", color = Color(0xFFB5BCD0))
            }
        }
    )

    if (directoryPickerOpen) {
        DirectoryBrowserDialog(
            managedAgentClient = client,
            initialPath = cwd.ifBlank { "" },
            onSelect = { selectedPath ->
                cwd = selectedPath
                directoryPickerOpen = false
            },
            onDismiss = { directoryPickerOpen = false }
        )
    }
}

@Composable
internal fun DirectoryBrowserDialog(
    managedAgentClient: ManagedAgentClient,
    initialPath: String,
    onSelect: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    var currentPath by remember { mutableStateOf(initialPath) }
    var parentPath by remember { mutableStateOf("") }
    var breadcrumbs by remember { mutableStateOf<List<Pair<String, String>>>(emptyList()) }
    var directories by remember { mutableStateOf<List<Pair<String, String>>>(emptyList()) }
    var loading by remember { mutableStateOf(false) }

    fun loadDir(path: String) {
        loading = true
        managedAgentClient.fetchDirectories(path) { cur, parent, bc, dirs ->
            runOnMainThread {
                currentPath = cur
                parentPath = parent
                breadcrumbs = bc
                directories = dirs
                loading = false
            }
        }
    }

    LaunchedEffect(Unit) {
        loadDir(initialPath)
    }

    Dialog(onDismissRequest = onDismiss) {
        Surface(
            modifier = Modifier
                .fillMaxWidth()
                .heightIn(min = 360.dp, max = 560.dp),
            shape = RoundedCornerShape(18.dp),
            color = Color(0xFF151925),
        ) {
            Column(Modifier.fillMaxSize().padding(16.dp)) {
                Row(
                    Modifier.fillMaxWidth(),
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    if (parentPath.isNotBlank()) {
                        IconButton(
                            onClick = { loadDir(parentPath) },
                            enabled = !loading
                        ) {
                            Icon(
                                Icons.AutoMirrored.Filled.ArrowBack,
                                contentDescription = "Go up",
                                tint = Color(0xFFF2F4FF)
                            )
                        }
                    }
                    Text(
                        "Select Directory",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = Color(0xFFF2F4FF),
                        modifier = Modifier.weight(1f)
                    )
                }

                // Current path indicator
                Text(
                    text = currentPath.ifBlank { "/" },
                    style = MaterialTheme.typography.bodySmall,
                    color = Color(0xFF9DA8FF),
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(vertical = 8.dp)
                )

                HorizontalDivider(color = Color(0xFF202638))

                if (loading) {
                    Box(
                        Modifier.weight(1f).fillMaxWidth(),
                        contentAlignment = Alignment.Center
                    ) {
                        CircularProgressIndicator(color = Color(0xFF9DA8FF))
                    }
                } else if (directories.isEmpty()) {
                    Box(
                        Modifier.weight(1f).fillMaxWidth(),
                        contentAlignment = Alignment.Center
                    ) {
                        Text("No subdirectories", color = Color(0xFFB5BCD0))
                    }
                } else {
                    LazyColumn(
                        modifier = Modifier.weight(1f).fillMaxWidth(),
                        verticalArrangement = Arrangement.spacedBy(4.dp)
                    ) {
                        items(directories, key = { it.second }) { (dirName, dirPath) ->
                            Row(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .clickable { loadDir(dirPath) }
                                    .padding(horizontal = 12.dp, vertical = 10.dp),
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                Icon(
                                    Icons.Default.Folder,
                                    contentDescription = null,
                                    tint = Color(0xFF78E0CD),
                                    modifier = Modifier.size(20.dp)
                                )
                                Spacer(Modifier.width(10.dp))
                                Text(
                                    text = dirName,
                                    style = MaterialTheme.typography.bodyMedium,
                                    color = Color(0xFFF2F4FF),
                                    modifier = Modifier.weight(1f),
                                    maxLines = 1,
                                    overflow = TextOverflow.Ellipsis
                                )
                            }
                        }
                    }
                }

                HorizontalDivider(color = Color(0xFF202638))

                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(top = 12.dp),
                    horizontalArrangement = Arrangement.End,
                    verticalAlignment = Alignment.CenterVertically
                ) {
                    TextButton(onClick = onDismiss) {
                        Text("Cancel", color = Color(0xFFB5BCD0))
                    }
                    Spacer(Modifier.width(8.dp))
                    Button(
                        onClick = { onSelect(currentPath) },
                        enabled = currentPath.isNotBlank() && !loading,
                        colors = ButtonDefaults.buttonColors(
                            containerColor = Color(0xFF9DA8FF),
                            contentColor = Color(0xFF0B0D14)
                        ),
                        shape = RoundedCornerShape(12.dp),
                    ) {
                        Text("Choose", fontWeight = FontWeight.Bold)
                    }
                }
            }
        }
    }
}

@Composable
internal fun TerminateWorkerDialog(
    peer: PeerStatus,
    client: ManagedAgentClient,
    onDismiss: () -> Unit,
    onTerminated: () -> Unit,
) {
    TerminateWorkerDialog(
        agentId = peer.id,
        cwd = peer.cwd,
        isWorktree = peer.worktree,
        managedAgentClient = client,
        onDismiss = onDismiss,
        onTerminated = onTerminated,
    )
}

@Composable
internal fun TerminateWorkerDialog(
    agentId: String,
    cwd: String,
    isWorktree: Boolean,
    managedAgentClient: ManagedAgentClient,
    onDismiss: () -> Unit,
    onTerminated: () -> Unit,
) {
    var hasUncommittedChanges by remember { mutableStateOf(false) }
    var cleanupWorktree by remember { mutableStateOf(true) }
    var terminatePending by remember { mutableStateOf(false) }
    var terminateError by remember { mutableStateOf<String?>(null) }
    var checkingWorktree by remember { mutableStateOf(isWorktree && cwd.isNotBlank()) }

    LaunchedEffect(agentId, cwd, isWorktree) {
        if (isWorktree && cwd.isNotBlank()) {
            checkingWorktree = true
            managedAgentClient.checkWorktree(cwd) { _, _, uncommitted ->
                runOnMainThread {
                    hasUncommittedChanges = uncommitted
                    checkingWorktree = false
                }
            }
        } else {
            checkingWorktree = false
        }
    }

    AlertDialog(
        onDismissRequest = { if (!terminatePending) onDismiss() },
        containerColor = Color(0xFF151925),
        titleContentColor = Color(0xFFF2F4FF),
        textContentColor = Color(0xFFF2F4FF),
        title = {
            Text("Terminate Worker", fontWeight = FontWeight.Bold)
        },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text(
                    "Terminate $agentId?",
                    style = MaterialTheme.typography.bodyLarge,
                    color = Color(0xFFF2F4FF)
                )

                if (isWorktree) {
                    if (hasUncommittedChanges) {
                        Card(
                            colors = CardDefaults.cardColors(
                                containerColor = Color(0xFF3B1E1E)
                            ),
                            shape = RoundedCornerShape(12.dp),
                            modifier = Modifier.fillMaxWidth()
                        ) {
                            Row(
                                modifier = Modifier.padding(12.dp),
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                Icon(
                                    Icons.Default.Warning,
                                    contentDescription = "Warning",
                                    tint = Color(0xFFFF8A80),
                                    modifier = Modifier.size(24.dp)
                                )
                                Spacer(Modifier.width(10.dp))
                                Text(
                                    "Warning: This worktree has uncommitted changes that will be lost.",
                                    style = MaterialTheme.typography.bodySmall,
                                    color = Color(0xFFFFB4AB),
                                    fontWeight = FontWeight.Medium
                                )
                            }
                        }
                    }

                    Row(
                        modifier = Modifier.fillMaxWidth(),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(
                                "Clean up worktree",
                                style = MaterialTheme.typography.bodyMedium,
                                fontWeight = FontWeight.SemiBold,
                                color = Color(0xFFF2F4FF)
                            )
                            Text(
                                "Deletes the worktree directory and branch",
                                style = MaterialTheme.typography.bodySmall,
                                color = Color(0xFFB5BCD0)
                            )
                        }
                        Switch(
                            checked = cleanupWorktree,
                            onCheckedChange = { cleanupWorktree = it },
                            enabled = !terminatePending,
                        )
                    }
                }

                if (terminateError != null) {
                    Text(
                        terminateError ?: "",
                        style = MaterialTheme.typography.bodySmall,
                        color = Color(0xFFFF8A80),
                    )
                }
            }
        },
        confirmButton = {
            Button(
                onClick = {
                    terminatePending = true
                    terminateError = null
                    managedAgentClient.terminateManagedAgent(
                        agentId = agentId,
                        cleanupWorktree = if (isWorktree) cleanupWorktree else false,
                    ) { success, error ->
                        runOnMainThread {
                            terminatePending = false
                            if (success) {
                                onTerminated()
                            } else {
                                terminateError = error ?: "Failed to terminate worker"
                            }
                        }
                    }
                },
                enabled = !terminatePending && !checkingWorktree,
                colors = ButtonDefaults.buttonColors(
                    containerColor = Color(0xFFBA1A1A),
                    contentColor = Color.White
                ),
                shape = RoundedCornerShape(12.dp),
            ) {
                Text(if (terminatePending) "Terminating…" else "Terminate", fontWeight = FontWeight.Bold)
            }
        },
        dismissButton = {
            TextButton(
                onClick = onDismiss,
                enabled = !terminatePending,
            ) {
                Text("Cancel", color = Color(0xFFB5BCD0))
            }
        }
    )
}
