package life.meyers.mecha

import java.util.Base64
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ImageAttachmentsTest {
    @Test
    fun acceptsSamsungStyleClipboardContentUrisFromEveryClipItemRepresentation() {
        assertEquals("content://media/external/images/42", acceptedImageUri("content://media/external/images/42", null, null))
        assertEquals("content://media/external/images/43", acceptedImageUri(null, "content://media/external/images/43", null))
        assertEquals("content://media/external/images/44", acceptedImageUri(null, null, "content://media/external/images/44"))
        assertEquals(null, acceptedImageUri(null, null, "https://untrusted.example/image.png"))
    }

    @Test
    fun buildsTheWebRouterAttachmentPayloadFromNormalizedWebp() {
        val webp = "RIFF\u0010\u0000\u0000\u0000WEBPVP8 ".toByteArray(Charsets.ISO_8859_1)

        val dataUrl = routerImageDataUrl(webp)

        assertTrue(dataUrl.startsWith("data:image/webp;base64,"))
        assertArrayEquals(webp, Base64.getDecoder().decode(dataUrl.substringAfter(',')))
        assertEquals(MAX_IMAGE_ATTACHMENTS * MAX_IMAGE_BYTES, MAX_IMAGE_TOTAL_BYTES)
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsNonWebpOutputBeforeBuildingTheRouterPayload() {
        routerImageDataUrl(ByteArray(16) { 1 })
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsConvertedImagesOverTheRouterPerImageLimit() {
        val oversized = ByteArray(MAX_IMAGE_BYTES + 1)
        "RIFF".toByteArray().copyInto(oversized, 0)
        "WEBP".toByteArray().copyInto(oversized, 8)
        routerImageDataUrl(oversized)
    }
}
