package life.meyers.mecha

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ForegroundNotificationPolicyTest {
    @Test
    fun inboundMessageWhileForegroundedUpdatesTheAppWithoutASystemNotification() {
        assertFalse(ForegroundNotificationPolicy.shouldPostInboundNotification(appIsForeground = true))
    }

    @Test
    fun inboundMessageWhileBackgroundedPostsASystemNotification() {
        assertTrue(ForegroundNotificationPolicy.shouldPostInboundNotification(appIsForeground = false))
    }
}
