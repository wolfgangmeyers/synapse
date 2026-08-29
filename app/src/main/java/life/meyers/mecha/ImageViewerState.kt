package life.meyers.mecha

internal const val IMAGE_VIEWER_MIN_SCALE = 1f
internal const val IMAGE_VIEWER_MAX_SCALE = 4f

internal data class ImageViewerState(
    val open: Boolean = false,
    val scale: Float = IMAGE_VIEWER_MIN_SCALE,
    val offsetX: Float = 0f,
    val offsetY: Float = 0f,
) {
    fun opened() = copy(open = true, scale = IMAGE_VIEWER_MIN_SCALE, offsetX = 0f, offsetY = 0f)

    fun zoomBy(factor: Float): ImageViewerState {
        require(factor.isFinite() && factor > 0f)
        return copy(scale = (scale * factor).coerceIn(IMAGE_VIEWER_MIN_SCALE, IMAGE_VIEWER_MAX_SCALE))
    }

    fun panBy(x: Float, y: Float): ImageViewerState {
        require(x.isFinite() && y.isFinite())
        return copy(offsetX = offsetX + x, offsetY = offsetY + y)
    }

    fun reset() = copy(scale = IMAGE_VIEWER_MIN_SCALE, offsetX = 0f, offsetY = 0f)

    fun closed() = ImageViewerState()
}
