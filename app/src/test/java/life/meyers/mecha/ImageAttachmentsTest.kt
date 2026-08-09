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
    fun buildsTheWebRouterAttachmentPayloadFromNormalizedJpeg() {
        val jpeg = byteArrayOf(0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xE0.toByte(), 0x00, 0x10)

        val dataUrl = routerImageDataUrl(jpeg)

        assertTrue(dataUrl.startsWith("data:image/jpeg;base64,"))
        assertArrayEquals(jpeg, Base64.getDecoder().decode(dataUrl.substringAfter(',')))
        assertEquals(MAX_IMAGE_ATTACHMENTS * MAX_IMAGE_BYTES, MAX_IMAGE_TOTAL_BYTES)
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsNonJpegOutputBeforeBuildingTheRouterPayload() {
        routerImageDataUrl(ByteArray(16) { 1 })
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsConvertedImagesOverTheRouterPerImageLimit() {
        val oversized = ByteArray(MAX_IMAGE_BYTES + 1)
        oversized[0] = 0xFF.toByte()
        oversized[1] = 0xD8.toByte()
        oversized[2] = 0xFF.toByte()
        routerImageDataUrl(oversized)
    }
}
