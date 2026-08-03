package life.meyers.mecha

import android.content.ContentResolver
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.Uri
import android.os.Build
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.util.Base64
import kotlin.math.max
import kotlin.math.roundToInt

internal const val MAX_IMAGE_ATTACHMENTS = 3
internal const val MAX_IMAGE_EDGE = 1600
internal const val MAX_IMAGE_BYTES = 768 * 1024
internal const val MAX_IMAGE_TOTAL_BYTES = MAX_IMAGE_ATTACHMENTS * MAX_IMAGE_BYTES
private const val MAX_IMAGE_INPUT_BYTES = 12 * 1024 * 1024
private const val MAX_IMAGE_PIXELS = 40_000_000L

internal data class PendingImage(
    val name: String,
    val webp: ByteArray,
) {
    val dataUrl: String get() = routerImageDataUrl(webp)
}

internal fun prepareImageAttachment(
    resolver: ContentResolver,
    uri: Uri,
    displayName: String = "image.webp",
): PendingImage {
    val input = resolver.openInputStream(uri) ?: throw IllegalArgumentException("Image could not be opened")
    val encoded = input.use { it.readBounded(MAX_IMAGE_INPUT_BYTES) }
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(encoded, 0, encoded.size, bounds)
    if (bounds.outWidth < 1 || bounds.outHeight < 1 || bounds.outWidth.toLong() * bounds.outHeight > MAX_IMAGE_PIXELS) {
        throw IllegalArgumentException("Image dimensions are invalid")
    }
    val bitmap = BitmapFactory.decodeByteArray(encoded, 0, encoded.size)
        ?: throw IllegalArgumentException("Image format is unsupported")
    return try {
        PendingImage(webpName(displayName), normalizeBitmap(bitmap))
    } finally {
        bitmap.recycle()
    }
}

internal fun acceptedImageUri(uri: String?, intentData: String?, text: String?): String? =
    sequenceOf(uri, intentData, text)
        .filterNotNull()
        .map(String::trim)
        .firstOrNull { value -> value.startsWith("content://") || value.startsWith("android.resource://") }

internal fun routerImageDataUrl(webp: ByteArray): String {
    require(webp.size in 1..MAX_IMAGE_BYTES) { "Image is too large after conversion" }
    require(webp.size >= 12 && webp.copyOfRange(0, 4).decodeToString() == "RIFF" && webp.copyOfRange(8, 12).decodeToString() == "WEBP") {
        "Converted image is not WebP"
    }
    return "data:image/webp;base64,${Base64.getEncoder().encodeToString(webp)}"
}

internal fun normalizeBitmap(source: Bitmap): ByteArray {
    var current = scaleToEdge(source, MAX_IMAGE_EDGE)
    try {
        repeat(5) { pass ->
            val quality = 82 - pass * 10
            val output = ByteArrayOutputStream()
            if (!current.compress(webpFormat(), quality, output)) throw IllegalArgumentException("Image could not be converted")
            val bytes = output.toByteArray()
            if (bytes.size <= MAX_IMAGE_BYTES) return bytes
            val nextWidth = max(1, (current.width * 0.8).roundToInt())
            val nextHeight = max(1, (current.height * 0.8).roundToInt())
            val scaled = Bitmap.createScaledBitmap(current, nextWidth, nextHeight, true)
            if (current !== source) current.recycle()
            current = scaled
        }
        throw IllegalArgumentException("Image is too large after conversion")
    } finally {
        if (current !== source) current.recycle()
    }
}

private fun scaleToEdge(source: Bitmap, maxEdge: Int): Bitmap {
    val edge = max(source.width, source.height)
    if (edge <= maxEdge) return source
    val scale = maxEdge.toDouble() / edge
    return Bitmap.createScaledBitmap(
        source,
        max(1, (source.width * scale).roundToInt()),
        max(1, (source.height * scale).roundToInt()),
        true,
    )
}

@Suppress("DEPRECATION")
private fun webpFormat(): Bitmap.CompressFormat =
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) Bitmap.CompressFormat.WEBP_LOSSY else Bitmap.CompressFormat.WEBP

private fun InputStream.readBounded(limit: Int): ByteArray {
    val output = ByteArrayOutputStream()
    val buffer = ByteArray(16 * 1024)
    var total = 0
    while (true) {
        val count = read(buffer)
        if (count < 0) break
        total += count
        if (total > limit) throw IllegalArgumentException("Image input is too large")
        output.write(buffer, 0, count)
    }
    if (total == 0) throw IllegalArgumentException("Image is empty")
    return output.toByteArray()
}

private fun webpName(value: String): String {
    val base = value.substringAfterLast('/').substringBeforeLast('.', value.substringAfterLast('/')).ifBlank { "image" }
    return "${base.replace(Regex("[^A-Za-z0-9._ -]"), "").take(110).ifBlank { "image" }}.webp"
}
