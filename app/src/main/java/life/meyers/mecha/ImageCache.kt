package life.meyers.mecha

import kotlinx.coroutines.suspendCancellableCoroutine
import okhttp3.Call
import okhttp3.Callback
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InterruptedIOException
import java.net.URI
import java.util.LinkedHashMap
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume

internal const val MAX_REMOTE_IMAGE_BYTES = MAX_IMAGE_BYTES
internal const val MAX_REMOTE_IMAGE_CACHE_BYTES = 32 * 1024 * 1024
internal const val MAX_REMOTE_IMAGE_CACHE_ENTRIES = 100
internal const val MAX_REMOTE_IMAGE_CACHE_AGE_MS = 30L * 24L * 60L * 60L * 1_000L
internal const val REMOTE_IMAGE_TIMEOUT_MS = 5_000L

internal enum class ImageLoadError {
    UNSAFE_URL,
    TOO_LARGE,
    INVALID_CONTENT,
    HTTP,
    TIMEOUT,
    NETWORK,
    CANCELLED,
}

internal sealed interface ImageFetchResult {
    data class Success(val bytes: ByteArray) : ImageFetchResult
    data class Failure(val error: ImageLoadError) : ImageFetchResult
}

internal data class ImageCacheKey(val routerIdentity: String, val url: String)

internal class BoundedImageCache(
    private val maxBytes: Int = MAX_REMOTE_IMAGE_CACHE_BYTES,
    private val maxEntries: Int = MAX_REMOTE_IMAGE_CACHE_ENTRIES,
    private val maxAgeMs: Long = MAX_REMOTE_IMAGE_CACHE_AGE_MS,
    private val nowMs: () -> Long = System::currentTimeMillis,
) {
    private data class Entry(val bytes: ByteArray, val storedAtMs: Long)

    private val entries = LinkedHashMap<ImageCacheKey, Entry>(16, 0.75f, true)
    private var bytes = 0

    init {
        require(maxBytes > 0)
        require(maxEntries > 0)
        require(maxAgeMs > 0)
    }

    @Synchronized
    fun get(routerIdentity: String, url: String): ByteArray? {
        evictExpired()
        return entries[ImageCacheKey(routerIdentity, url)]?.bytes?.copyOf()
    }

    @Synchronized
    fun put(routerIdentity: String, url: String, value: ByteArray): Boolean {
        evictExpired()
        if (value.isEmpty() || value.size > maxBytes) return false
        val key = ImageCacheKey(routerIdentity, url)
        entries.remove(key)?.let { bytes -= it.bytes.size }
        entries[key] = Entry(value.copyOf(), nowMs())
        bytes += value.size
        evictIfNeeded()
        return entries.containsKey(key)
    }

    @Synchronized
    fun remove(routerIdentity: String, url: String) {
        entries.remove(ImageCacheKey(routerIdentity, url))?.let { bytes -= it.bytes.size }
    }

    @Synchronized
    fun clearRouter(routerIdentity: String) {
        entries.keys.filter { it.routerIdentity == routerIdentity }.forEach { key ->
            entries.remove(key)?.let { bytes -= it.bytes.size }
        }
    }

    @Synchronized
    fun sizeBytes(): Int {
        evictExpired()
        return bytes
    }

    @Synchronized
    fun size(): Int {
        evictExpired()
        return entries.size
    }

    @Synchronized
    fun keys(): List<ImageCacheKey> {
        evictExpired()
        return entries.keys.toList()
    }

    private fun evictExpired() {
        val now = nowMs()
        entries.entries.removeIf { (_, entry) ->
            val expired = now >= entry.storedAtMs && now - entry.storedAtMs >= maxAgeMs
            if (expired) bytes -= entry.bytes.size
            expired
        }
    }

    private fun evictIfNeeded() {
        while (bytes > maxBytes || entries.size > maxEntries) {
            val oldest = entries.entries.iterator().next()
            bytes -= oldest.value.bytes.size
            entries.remove(oldest.key)
        }
    }
}

internal interface ImageRequestHandle {
    fun cancel()
}

internal fun interface ImageFetcher {
    fun fetch(url: String, callback: (ImageFetchResult) -> Unit): ImageRequestHandle
}

internal class RemoteImageLoader(
    private val fetcher: ImageFetcher,
    private val cache: BoundedImageCache = BoundedImageCache(),
) {
    private data class Waiter(
        val expectedMimeType: String?,
        val callback: (ImageFetchResult) -> Unit,
    )
    private class Pending(val waiters: LinkedHashMap<Long, Waiter> = LinkedHashMap()) {
        var request: ImageRequestHandle? = null
    }

    private val lock = Any()
    private val inFlight = mutableMapOf<ImageCacheKey, Pending>()
    private var waiterSequence = 0L

    fun request(
        routerIdentity: String,
        routerBase: String,
        url: String,
        expectedMimeType: String?,
        callback: (ImageFetchResult) -> Unit,
    ): ImageRequestHandle {
        val safeUrl = safeImageUrl(routerBase, url)
        if (safeUrl == null) {
            callback(ImageFetchResult.Failure(ImageLoadError.UNSAFE_URL))
            return NoOpImageRequestHandle
        }
        val key = ImageCacheKey(routerIdentity, safeUrl)
        cache.get(routerIdentity, safeUrl)?.let { bytes ->
            val result = validateImageBytes(bytes, expectedMimeType)
            if (result is ImageFetchResult.Success) {
                callback(result)
                return NoOpImageRequestHandle
            }
            cache.remove(routerIdentity, safeUrl)
        }

        val waiterId: Long
        val pending: Pending
        val startFetch: Boolean
        synchronized(lock) {
            waiterId = ++waiterSequence
            pending = inFlight[key] ?: Pending().also { inFlight[key] = it }
            pending.waiters[waiterId] = Waiter(expectedMimeType, callback)
            startFetch = pending.request == null && pending.waiters.size == 1
        }
        if (startFetch) {
            val request = runCatching {
                fetcher.fetch(safeUrl) { result -> finish(key, result) }
            }.getOrElse {
                finish(key, ImageFetchResult.Failure(ImageLoadError.NETWORK))
                null
            }
            if (request != null) synchronized(lock) {
                pending.request = request
                if (pending.waiters.isEmpty() && inFlight[key] === pending) {
                    inFlight.remove(key)
                    request.cancel()
                }
            }
        }
        return object : ImageRequestHandle {
            private var cancelled = false
            override fun cancel() {
                synchronized(lock) {
                    if (cancelled) return
                    cancelled = true
                    val current = inFlight[key] ?: return
                    current.waiters.remove(waiterId)
                    if (current.waiters.isEmpty() && inFlight[key] === current) {
                        inFlight.remove(key)
                        current.request?.cancel()
                    }
                }
            }
        }
    }

    suspend fun load(
        routerIdentity: String,
        routerBase: String,
        url: String,
        expectedMimeType: String?,
    ): ImageFetchResult = suspendCancellableCoroutine { continuation ->
        val handle = request(routerIdentity, routerBase, url, expectedMimeType) { result ->
            if (continuation.isActive) continuation.resume(result)
        }
        continuation.invokeOnCancellation { handle.cancel() }
    }

    private fun finish(key: ImageCacheKey, fetched: ImageFetchResult) {
        val pending = synchronized(lock) { inFlight.remove(key) ?: return }
        val cached = when (fetched) {
            is ImageFetchResult.Failure -> fetched
            is ImageFetchResult.Success -> validateImageBytes(fetched.bytes, null).also { validated ->
                if (validated is ImageFetchResult.Success) cache.put(key.routerIdentity, key.url, validated.bytes)
            }
        }
        pending.waiters.values.forEach { waiter ->
            val result = when (cached) {
                is ImageFetchResult.Failure -> cached
                is ImageFetchResult.Success -> validateImageBytes(cached.bytes, waiter.expectedMimeType)
            }
            waiter.callback(result)
        }
    }

    private companion object {
        val NoOpImageRequestHandle = object : ImageRequestHandle {
            override fun cancel() = Unit
        }
    }
}

internal fun validateImageBytes(bytes: ByteArray, expectedMimeType: String? = null): ImageFetchResult {
    if (bytes.isEmpty() || bytes.size > MAX_REMOTE_IMAGE_BYTES) return ImageFetchResult.Failure(
        if (bytes.size > MAX_REMOTE_IMAGE_BYTES) ImageLoadError.TOO_LARGE else ImageLoadError.INVALID_CONTENT,
    )
    val actual = sniffImageMime(bytes) ?: return ImageFetchResult.Failure(ImageLoadError.INVALID_CONTENT)
    val expected = expectedMimeType?.substringBefore(';')?.trim()?.lowercase()?.takeIf { it.isNotBlank() }
    if (expected != null && expected != actual) return ImageFetchResult.Failure(ImageLoadError.INVALID_CONTENT)
    return ImageFetchResult.Success(bytes.copyOf())
}

internal fun sniffImageMime(bytes: ByteArray): String? = when {
    bytes.size >= 3 && bytes[0] == 0xFF.toByte() && bytes[1] == 0xD8.toByte() && bytes[2] == 0xFF.toByte() -> "image/jpeg"
    bytes.size >= 8 && bytes.copyOfRange(0, 8).contentEquals(byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)) -> "image/png"
    bytes.size >= 6 && (bytes.copyOfRange(0, 6).contentEquals("GIF87a".toByteArray()) || bytes.copyOfRange(0, 6).contentEquals("GIF89a".toByteArray())) -> "image/gif"
    bytes.size >= 12 && bytes.copyOfRange(0, 4).contentEquals("RIFF".toByteArray()) && bytes.copyOfRange(8, 12).contentEquals("WEBP".toByteArray()) -> "image/webp"
    else -> null
}

internal fun safeImageUrl(routerBase: String, candidate: String): String? = runCatching {
    val base = URI(routerBase)
    if (base.scheme !in setOf("http", "https") || base.host.isNullOrBlank()
        || base.userInfo != null || base.query != null || base.fragment != null
    ) return@runCatching null
    val parsed = URI(candidate)
    val resolved = if (parsed.isAbsolute) parsed else base.resolve(parsed)
    if (resolved.scheme != base.scheme || !resolved.host.equals(base.host, ignoreCase = true)
        || resolved.port != base.port || resolved.userInfo != null || resolved.fragment != null
        || resolved.path.isNullOrBlank() || resolved.query != null && resolved.query.length > 512
    ) return@runCatching null
    resolved.toString()
}.getOrNull()

internal class OkHttpImageFetcher(client: OkHttpClient) : ImageFetcher {
    private val client = client.newBuilder()
        .retryOnConnectionFailure(false)
        .followRedirects(false)
        .followSslRedirects(false)
        .connectTimeout(REMOTE_IMAGE_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        .readTimeout(REMOTE_IMAGE_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        .callTimeout(REMOTE_IMAGE_TIMEOUT_MS, TimeUnit.MILLISECONDS)
        .build()

    override fun fetch(url: String, callback: (ImageFetchResult) -> Unit): ImageRequestHandle {
        val call = client.newCall(Request.Builder().url(url).get().build())
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, error: IOException) {
                callback(ImageFetchResult.Failure(if (call.isCanceled()) ImageLoadError.CANCELLED else if (error is InterruptedIOException) ImageLoadError.TIMEOUT else ImageLoadError.NETWORK))
            }

            override fun onResponse(call: Call, response: Response) {
                response.use {
                    if (!response.isSuccessful) {
                        callback(ImageFetchResult.Failure(ImageLoadError.HTTP))
                        return
                    }
                    val length = response.body?.contentLength() ?: -1L
                    if (length > MAX_REMOTE_IMAGE_BYTES) {
                        callback(ImageFetchResult.Failure(ImageLoadError.TOO_LARGE))
                        return
                    }
                    val body = response.body ?: run {
                        callback(ImageFetchResult.Failure(ImageLoadError.INVALID_CONTENT))
                        return
                    }
                    val bytes = runCatching {
                        body.byteStream().use { input ->
                            val output = ByteArrayOutputStream()
                            val buffer = ByteArray(16 * 1024)
                            var total = 0
                            while (true) {
                                val count = input.read(buffer)
                                if (count < 0) break
                                total += count
                                if (total > MAX_REMOTE_IMAGE_BYTES) return@runCatching null
                                output.write(buffer, 0, count)
                            }
                            output.toByteArray()
                        }
                    }.getOrNull()
                    if (bytes == null) callback(ImageFetchResult.Failure(ImageLoadError.TOO_LARGE))
                    else callback(validateImageBytes(bytes, response.header("Content-Type")))
                }
            }
        })
        return object : ImageRequestHandle {
            override fun cancel() = call.cancel()
        }
    }
}
