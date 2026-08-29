package life.meyers.mecha

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ImageViewerStateTest {
    @Test
    fun openingStartsAtOneAndZoomAndPanAreApplied() {
        val state = ImageViewerState().opened().zoomBy(2f).panBy(12f, -4f)

        assertTrue(state.open)
        assertEquals(2f, state.scale)
        assertEquals(12f, state.offsetX)
        assertEquals(-4f, state.offsetY)
    }

    @Test
    fun zoomIsBoundedAndResetKeepsViewerOpen() {
        val state = ImageViewerState().opened().zoomBy(100f).zoomBy(0.001f).reset()

        assertEquals(1f, state.scale)
        assertEquals(0f, state.offsetX)
        assertEquals(0f, state.offsetY)
        assertTrue(state.open)
    }

    @Test
    fun closingCleansUpViewerState() {
        val closed = ImageViewerState().opened().zoomBy(2f).panBy(5f, 5f).closed()

        assertFalse(closed.open)
        assertEquals(ImageViewerState(), closed)
    }
}
