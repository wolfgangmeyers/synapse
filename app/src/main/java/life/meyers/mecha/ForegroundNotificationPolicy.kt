package life.meyers.mecha

internal object ForegroundNotificationPolicy {
    fun shouldPostInboundNotification(appIsForeground: Boolean): Boolean = !appIsForeground
}
