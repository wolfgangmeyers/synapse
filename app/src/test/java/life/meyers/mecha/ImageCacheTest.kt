package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ImageCacheTest {
    @Test
    fun defaultCacheUsesTheRequiredEntryByteAndAgeBounds() {
        assertEquals(100, MAX_REMOTE_IMAGE_CACHE_ENTRIES)
        assertEquals(32 * 1024 * 1024, MAX_REMOTE_IMAGE_CACHE_BYTES)
        assertEquals(30L * 24L * 60L * 60L * 1_000L, MAX_REMOTE_IMAGE_CACHE_AGE_MS)

        var now = 1_000L
        val cache = BoundedImageCache(nowMs = { now })
        repeat(MAX_REMOTE_IMAGE_CACHE_ENTRIES) { index ->
            assertTrue(cache.put("router-a", "/$index", byteArrayOf(index.toByte())))
        }
        assertEquals(MAX_REMOTE_IMAGE_CACHE_ENTRIES, cache.size())
        assertTrue(cache.put("router-a", "/new", byteArrayOf(1)))
        assertEquals(MAX_REMOTE_IMAGE_CACHE_ENTRIES, cache.size())
        assertNull(cache.get("router-a", "/0"))
        assertNotNull(cache.get("router-a", "/new"))
        assertEquals(MAX_REMOTE_IMAGE_CACHE_ENTRIES, cache.keys().count { it.routerIdentity == "router-a" })
        now += MAX_REMOTE_IMAGE_CACHE_AGE_MS
        assertEquals(0, cache.size())
        assertEquals(0, cache.sizeBytes())
    }

    @Test
    fun ageExpiryIsDeterministicAndAccessDoesNotRefreshStoredAge() {
        var now = 10_000L
        val cache = BoundedImageCache(maxBytes = 100, maxEntries = 4, maxAgeMs = 100, nowMs = { now })
        cache.put("router-a", "/same", byteArrayOf(1, 2))
        now += 99
        assertNotNull(cache.get("router-a", "/same"))
        now += 1
        assertNull(cache.get("router-a", "/same"))
        assertEquals(0, cache.sizeBytes())
    }

    @Test
    fun evictsLeastRecentlyUsedEntriesAtTheByteAndEntryCaps() {
        val cache = BoundedImageCache(maxBytes = 5, maxEntries = 2)
        assertTrue(cache.put("router-a", "/one", byteArrayOf(1, 2, 3)))
        assertTrue(cache.put("router-a", "/two", byteArrayOf(4)))
        assertNotNull(cache.get("router-a", "/one"))

        assertTrue(cache.put("router-a", "/three", byteArrayOf(5, 6)))

        assertNotNull(cache.get("router-a", "/one"))
        assertNull(cache.get("router-a", "/two"))
        assertEquals(5, cache.sizeBytes())
        assertEquals(2, cache.size())
    }

    @Test
    fun cacheEntriesAreScopedToRouterIdentity() {
        val cache = BoundedImageCache(maxBytes = 10, maxEntries = 4)
        cache.put("router-a", "/same", byteArrayOf(1))
        cache.put("router-b", "/same", byteArrayOf(2))

        assertEquals(byteArrayOf(1).toList(), cache.get("router-a", "/same")?.toList())
        assertEquals(byteArrayOf(2).toList(), cache.get("router-b", "/same")?.toList())
        cache.clearRouter("router-a")
        assertNull(cache.get("router-a", "/same"))
        assertNotNull(cache.get("router-b", "/same"))
    }

    @Test
    fun deduplicatesConcurrentLoadsAndCancelsOnlyAfterLastConsumerLeaves() {
        val fetcher = FakeImageFetcher()
        val loader = RemoteImageLoader(fetcher, BoundedImageCache(100, 4))
        val results = mutableListOf<ImageFetchResult>()
        val first = loader.request("router-a", "https://router.example", "/v1/attachments/a.jpg", "image/jpeg") { results += it }
        val second = loader.request("router-a", "https://router.example", "/v1/attachments/a.jpg", "image/jpeg") { results += it }

        assertEquals(1, fetcher.fetchCount)
        first.cancel()
        assertFalse(fetcher.cancelled)
        second.cancel()
        assertTrue(fetcher.cancelled)
        fetcher.complete(ImageFetchResult.Success(jpeg()))
        assertTrue(results.isEmpty())
    }

    @Test
    fun deduplicationDoesNotCrossRouterIdentity() {
        val fetcher = FakeImageFetcher()
        val loader = RemoteImageLoader(fetcher, BoundedImageCache(100, 4))
        loader.request("router-a", "https://router.example", "/v1/attachments/a.jpg", "image/jpeg") { }
        loader.request("router-b", "https://router.example", "/v1/attachments/a.jpg", "image/jpeg") { }

        assertEquals(2, fetcher.fetchCount)
    }

    @Test
    fun preservesTimeoutAndNetworkFailureStatesForTheUi() {
        val fetcher = FakeImageFetcher()
        val loader = RemoteImageLoader(fetcher, BoundedImageCache(100, 4))
        val results = mutableListOf<ImageFetchResult>()
        loader.request("router-a", "https://router.example", "/v1/attachments/a.jpg", "image/jpeg") { results += it }

        fetcher.complete(ImageFetchResult.Failure(ImageLoadError.TIMEOUT))

        assertEquals(ImageLoadError.TIMEOUT, (results.single() as ImageFetchResult.Failure).error)
    }

    @Test
    fun rejectsMalformedOversizeAndUnsafeImageInputs() {
        assertEquals(ImageLoadError.INVALID_CONTENT, validateFailure(byteArrayOf(1, 2, 3)))
        assertEquals(ImageLoadError.TOO_LARGE, validateFailure(ByteArray(MAX_REMOTE_IMAGE_BYTES + 1)))
        assertNull(safeImageUrl("https://router.example", "https://attacker.example/v1/attachments/a.jpg"))
        assertNull(safeImageUrl("https://router.example", "data:image/jpeg;base64,AAAA"))
        assertEquals("https://router.example/v1/attachments/a.jpg", safeImageUrl("https://router.example", "/v1/attachments/a.jpg"))
    }

    @Test
    fun validatesContentAgainstTheDeclaredMimeType() {
        assertEquals(ImageLoadError.INVALID_CONTENT, validateFailure(jpeg(), "image/png"))
        assertEquals(ImageLoadError.INVALID_CONTENT, validateFailure(jpeg(), "text/plain"))
        assertTrue(validateImageForTest(jpeg(), "image/jpeg"))
    }

    private fun validateFailure(bytes: ByteArray, mime: String? = null): ImageLoadError? = when (val result = validateImageBytes(bytes, mime)) {
        is ImageFetchResult.Failure -> result.error
        is ImageFetchResult.Success -> null
    }

    private fun validateImageForTest(bytes: ByteArray, mime: String): Boolean = validateFailure(bytes, mime) == null

    private fun jpeg() = byteArrayOf(0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xE0.toByte())

    private class FakeImageFetcher : ImageFetcher {
        var fetchCount = 0
        var cancelled = false
        private var callback: ((ImageFetchResult) -> Unit)? = null

        override fun fetch(url: String, callback: (ImageFetchResult) -> Unit): ImageRequestHandle {
            fetchCount++
            this.callback = callback
            return object : ImageRequestHandle {
                override fun cancel() {
                    cancelled = true
                }
            }
        }

        fun complete(result: ImageFetchResult) {
            callback?.invoke(result)
        }
    }
}
