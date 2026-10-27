package life.meyers.mecha

import android.app.Activity
import android.app.Instrumentation
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Paint
import android.os.Bundle
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.graphics.asAndroidColorFilter

/** Pixel proof uses the same Compose tint adapter as the avatar, on the Android renderer. */
class AvatarAlphaInstrumentation : Instrumentation() {
    override fun onCreate(arguments: Bundle?) {
        super.onCreate(arguments)
        start()
    }

    override fun onStart() {
        val result = Bundle()
        try {
            val source = targetContext.assets.open("robot062.png").use { BitmapFactory.decodeStream(it) }
            fun render(mode: BlendMode): Bitmap {
                val output = Bitmap.createBitmap(source.width, source.height, Bitmap.Config.ARGB_8888)
                val paint = Paint().apply {
                    colorFilter = ColorFilter.tint(Color(0xFF93E25A), mode).asAndroidColorFilter()
                }
                Canvas(output).drawBitmap(source, 0f, 0f, paint)
                return output
            }
            val actual = render(BlendMode.Modulate)
            val old = render(BlendMode.Color)
            var transparent = 0
            var partial = 0
            var recolored = 0
            var oldMismatches = 0
            val opaqueColors = mutableSetOf<Int>()
            for (y in 0 until source.height) for (x in 0 until source.width) {
                val before = source.getPixel(x, y)
                val after = actual.getPixel(x, y)
                val alpha = before ushr 24
                check(alpha == after ushr 24) { "Alpha changed at $x,$y" }
                if (alpha == 0) transparent++
                if (alpha in 1..254) partial++
                if (alpha == 255) opaqueColors.add(after)
                if (alpha == 255 && before != after) recolored++
                if (alpha != old.getPixel(x, y) ushr 24) oldMismatches++
            }
            check(transparent > 0 && partial > 0 && recolored > 0 && oldMismatches > 0 && opaqueColors.size > 100)
            result.putString("pixels", "transparent=$transparent partial=$partial recolored=$recolored oldAlphaFailures=$oldMismatches detailColors=${opaqueColors.size}")
            source.recycle(); actual.recycle(); old.recycle()
            finish(Activity.RESULT_OK, result)
        } catch (failure: Throwable) {
            result.putString("failure", failure.stackTraceToString())
            finish(Activity.RESULT_CANCELED, result)
        }
    }
}
