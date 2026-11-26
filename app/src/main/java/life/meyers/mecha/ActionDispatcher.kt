package life.meyers.mecha

import androidx.compose.runtime.staticCompositionLocalOf
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.MultipartBody
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/**
 * ActionDispatcher handles HTTP transport between SDUI components and the Go router.
 */
class ActionDispatcher(
    val baseUrl: String,
    val state: SduiState,
    val slotController: SlotController,
    val onUpdateSurface: ((Surface) -> Unit)? = null,
    val onPushSurface: ((Surface) -> Unit)? = null,
    val client: OkHttpClient = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .writeTimeout(30, TimeUnit.SECONDS)
        .build(),
    val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
    val mainDispatcher: CoroutineDispatcher = Dispatchers.Main,
    private val scope: CoroutineScope = CoroutineScope(mainDispatcher),
) {
    fun resolveUrl(url: String): String {
        val trimmed = url.trim()
        if (trimmed.startsWith("http://", ignoreCase = true) || trimmed.startsWith("https://", ignoreCase = true)) {
            return trimmed
        }
        val base = baseUrl.trimEnd('/')
        val path = trimmed.trimStart('/')
        return "$base/$path"
    }

    suspend fun fetchSurface(url: String): Surface = withContext(ioDispatcher) {
        val resolved = resolveUrl(url)
        val request = Request.Builder().url(resolved).get().build()
        client.newCall(request).execute().use { response ->
            if (!response.isSuccessful) {
                throw IllegalStateException("HTTP ${response.code}: ${response.message}")
            }
            val body = response.body?.string() ?: throw IllegalStateException("Empty response body")
            Surface.fromJson(body)
        }
    }

    suspend fun dispatchAction(payload: ActionPayload): ActionResponse = withContext(ioDispatcher) {
        val url = resolveUrl("/v1/ui/action")
        val mediaType = "application/json; charset=utf-8".toMediaType()
        val json = payload.toJson()
        val request = Request.Builder()
            .url(url)
            .post(json.toRequestBody(mediaType))
            .build()

        val resp = client.newCall(request).execute().use { response ->
            if (!response.isSuccessful) {
                throw IllegalStateException("HTTP ${response.code}: ${response.message}")
            }
            val body = response.body?.string() ?: throw IllegalStateException("Empty response body")
            ActionResponse.fromJson(body)
        }

        withContext(mainDispatcher) {
            handleActionResponse(resp)
        }
        resp
    }

    fun dispatch(
        actionId: String,
        blockId: String? = null,
        agentId: String? = null,
        surfaceId: String? = null,
        value: String? = null,
        slotId: String? = null,
        onError: ((Throwable) -> Unit)? = null,
    ) {
        scope.launch {
            try {
                val payload = ActionPayload(
                    actionId = actionId,
                    blockId = blockId,
                    agentId = agentId,
                    surfaceId = surfaceId,
                    value = value,
                    slotId = slotId,
                    values = state.toSnapshot(),
                )
                dispatchAction(payload)
            } catch (e: Throwable) {
                onError?.invoke(e)
            }
        }
    }

    fun handleActionResponse(response: ActionResponse) {
        when (response.responseAction) {
            ResponseAction.UPDATE -> {
                response.surface?.let { onUpdateSurface?.invoke(it) }
            }
            ResponseAction.UPDATE_SLOT -> {
                val sId = response.slotId
                val vUrl = response.viewUrl
                if (!sId.isNullOrBlank() && !vUrl.isNullOrBlank()) {
                    slotController.setSlotUrl(sId, vUrl)
                }
            }
            ResponseAction.PUSH -> {
                response.surface?.let { onPushSurface?.invoke(it) }
            }
            ResponseAction.CLEAR_INPUTS -> {
                state.clearInputs(response.clearInputs)
            }
            ResponseAction.ERRORS -> {
                state.setErrors(response.errors)
            }
        }
    }

    suspend fun uploadAttachment(
        endpoint: String,
        bytes: ByteArray,
        filename: String,
        mimeType: String = "image/png"
    ): Pair<String, String> = withContext(Dispatchers.IO) {
        val url = resolveUrl(endpoint)
        val body = MultipartBody.Builder()
            .setType(MultipartBody.FORM)
            .addFormDataPart(
                "file",
                filename,
                bytes.toRequestBody(mimeType.toMediaType())
            )
            .build()
        val request = Request.Builder().url(url).post(body).build()
        client.newCall(request).execute().use { resp ->
            if (!resp.isSuccessful) {
                throw IllegalStateException("Upload failed with HTTP ${resp.code}")
            }
            val resStr = resp.body?.string() ?: throw IllegalStateException("Empty upload response")
            val obj = JSONObject(resStr)
            val attachmentId = obj.optString("attachment_id", "")
            val thumbUrl = obj.optString("thumbnail_url", "")
            Pair(attachmentId, thumbUrl)
        }
    }
}

val LocalActionDispatcher = staticCompositionLocalOf<ActionDispatcher> {
    error("No ActionDispatcher provided")
}
