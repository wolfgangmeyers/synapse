package life.meyers.mecha

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.net.URLEncoder
import java.util.UUID

internal class ManagedAgentClient(
    private val client: OkHttpClient,
    private val routerBaseUrlProvider: () -> String,
) {
    constructor(routerBaseUrl: String, client: OkHttpClient = OkHttpClient()) : this(client, { routerBaseUrl })
    fun checkWorktree(
        path: String,
        onResult: (isGitRepo: Boolean, isMainWorktree: Boolean, hasUncommittedChanges: Boolean) -> Unit,
    ) {
        val base = routerBaseUrlProvider()
        if (base.isBlank()) {
            onResult(false, false, false)
            return
        }
        Thread {
            val encoded = runCatching { URLEncoder.encode(path, "UTF-8") }.getOrDefault(path)
            val url = "$base/v1/git/worktree-check?path=$encoded"
            val request = Request.Builder().url(url).get().build()
            try {
                client.newCall(request).execute().use { response ->
                    if (!response.isSuccessful) {
                        onResult(false, false, false)
                        return@use
                    }
                    val bodyStr = response.body?.string().orEmpty()
                    val json = JSONObject(bodyStr)
                    val isGit = json.optBoolean("is_git_repo", false)
                    val isMain = json.optBoolean("is_main_worktree", false)
                    val hasUncommitted = json.optBoolean("has_uncommitted_changes", false)
                    onResult(isGit, isMain, hasUncommitted)
                }
            } catch (_: Exception) {
                onResult(false, false, false)
            }
        }.start()
    }

    fun fetchModels(
        onResult: (defaultModel: ModelSummary?, models: List<ModelSummary>) -> Unit,
    ) {
        val base = routerBaseUrlProvider()
        if (base.isBlank()) {
            onResult(null, emptyList())
            return
        }
        Thread {
            val url = "$base/v1/models"
            val request = Request.Builder().url(url).get().build()
            try {
                client.newCall(request).execute().use { response ->
                    if (!response.isSuccessful) {
                        onResult(null, emptyList())
                        return@use
                    }
                    val bodyStr = response.body?.string().orEmpty()
                    val json = JSONObject(bodyStr)
                    val defModelObj = json.optJSONObject("default_model")
                    val defaultModel = defModelObj?.let {
                        ModelSummary(
                            provider = it.optString("provider"),
                            id = it.optString("id"),
                            name = it.optString("name").ifBlank { it.optString("id") }
                        )
                    }
                    val modelsArray = json.optJSONArray("models") ?: JSONArray()
                    val modelsList = mutableListOf<ModelSummary>()
                    for (i in 0 until modelsArray.length()) {
                        val mObj = modelsArray.optJSONObject(i) ?: continue
                        val available = mObj.optBoolean("available", true)
                        if (available) {
                            modelsList.add(
                                ModelSummary(
                                    provider = mObj.optString("provider"),
                                    id = mObj.optString("id"),
                                    name = mObj.optString("name").ifBlank { mObj.optString("id") }
                                )
                            )
                        }
                    }
                    onResult(defaultModel, modelsList)
                }
            } catch (_: Exception) {
                onResult(null, emptyList())
            }
        }.start()
    }

    fun fetchDirectories(
        path: String,
        onResult: (currentPath: String, parentPath: String, breadcrumbs: List<Pair<String, String>>, dirs: List<Pair<String, String>>) -> Unit,
    ) {
        val base = routerBaseUrlProvider()
        if (base.isBlank()) {
            onResult("", "", emptyList(), emptyList())
            return
        }
        Thread {
            val encoded = runCatching { URLEncoder.encode(path, "UTF-8") }.getOrDefault(path)
            val url = "$base/v1/managed-directories?path=$encoded"
            val request = Request.Builder().url(url).get().build()
            try {
                client.newCall(request).execute().use { response ->
                    if (!response.isSuccessful) {
                        onResult(path, "", emptyList(), emptyList())
                        return@use
                    }
                    val bodyStr = response.body?.string().orEmpty()
                    val json = JSONObject(bodyStr)
                    val currentPath = json.optString("path", path)
                    val parent = json.optString("parent", "")
                    val bcList = mutableListOf<Pair<String, String>>()
                    val bcArray = json.optJSONArray("breadcrumbs")
                    if (bcArray != null) {
                        for (i in 0 until bcArray.length()) {
                            val b = bcArray.optJSONObject(i) ?: continue
                            val name = b.optString("name")
                            val p = b.optString("path")
                            if (name.isNotBlank() && p.isNotBlank()) {
                                bcList.add(name to p)
                            }
                        }
                    }
                    val dirList = mutableListOf<Pair<String, String>>()
                    val dirArray = json.optJSONArray("directories")
                    if (dirArray != null) {
                        for (i in 0 until dirArray.length()) {
                            val d = dirArray.optJSONObject(i) ?: continue
                            val name = d.optString("name")
                            val p = d.optString("path")
                            if (name.isNotBlank() && p.isNotBlank()) {
                                dirList.add(name to p)
                            }
                        }
                    }
                    onResult(currentPath, parent, bcList, dirList)
                }
            } catch (_: Exception) {
                onResult(path, "", emptyList(), emptyList())
            }
        }.start()
    }

    fun launchManagedAgent(
        name: String,
        cwd: String,
        model: ModelSummary?,
        instruction: String,
        worktree: Boolean,
        worktreeBranch: String,
        onComplete: (success: Boolean, error: String?) -> Unit,
    ) {
        val base = routerBaseUrlProvider()
        if (base.isBlank()) {
            onComplete(false, "No active router connected")
            return
        }
        Thread {
            try {
                val payload = JSONObject()
                payload.put("name", normalizeManagedAgentName(name))
                payload.put("cwd", cwd.trim())
                payload.put("runtime", "embedded")
                if (model != null) {
                    val m = JSONObject()
                    m.put("provider", model.provider)
                    m.put("id", model.id)
                    payload.put("model", m)
                }
                if (instruction.isNotBlank()) {
                    payload.put("initial_instruction", instruction.trim())
                }
                if (worktree) {
                    payload.put("worktree", true)
                    if (worktreeBranch.isNotBlank()) {
                        payload.put("worktree_branch", worktreeBranch.trim())
                    }
                }

                val reqId = UUID.randomUUID().toString()
                val request = Request.Builder()
                    .url("$base/v1/managed-agents")
                    .header("Content-Type", "application/json")
                    .header("X-Request-ID", reqId)
                    .post(payload.toString().toRequestBody("application/json".toMediaType()))
                    .build()

                client.newCall(request).execute().use { response ->
                    if (response.isSuccessful || response.code == 202) {
                        onComplete(true, null)
                    } else {
                        val body = response.body?.string().orEmpty()
                        val msg = parseErrorMessage(body, "Launch failed (${response.code})")
                        onComplete(false, msg)
                    }
                }
            } catch (e: Exception) {
                onComplete(false, e.message ?: "Failed to connect to router")
            }
        }.start()
    }

    fun terminateManagedAgent(
        agentId: String,
        cleanupWorktree: Boolean,
        onComplete: (success: Boolean, error: String?) -> Unit,
    ) {
        val base = routerBaseUrlProvider()
        if (base.isBlank()) {
            onComplete(false, "No active router connected")
            return
        }
        Thread {
            try {
                val reqId = UUID.randomUUID().toString()
                val encodedId = runCatching { URLEncoder.encode(agentId, "UTF-8") }.getOrDefault(agentId)
                val url = "$base/v1/managed-agents/$encodedId?cleanup_worktree=$cleanupWorktree"
                val emptyBody = ByteArray(0).toRequestBody(null, 0, 0)
                val request = Request.Builder()
                    .url(url)
                    .header("X-Request-ID", reqId)
                    .delete(emptyBody)
                    .build()

                client.newCall(request).execute().use { response ->
                    if (response.isSuccessful || response.code == 202) {
                        onComplete(true, null)
                    } else {
                        val body = response.body?.string().orEmpty()
                        val msg = parseErrorMessage(body, "Termination failed (${response.code})")
                        onComplete(false, msg)
                    }
                }
            } catch (e: Exception) {
                onComplete(false, e.message ?: "Failed to connect to router")
            }
        }.start()
    }

    private fun parseErrorMessage(body: String, fallback: String): String {
        return runCatching {
            val json = JSONObject(body)
            val errObj = json.optJSONObject("error")
            errObj?.optString("message")?.takeIf { it.isNotBlank() }
                ?: json.optString("message").takeIf { it.isNotBlank() }
                ?: fallback
        }.getOrDefault(fallback)
    }
}
