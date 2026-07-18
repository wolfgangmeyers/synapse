package life.meyers.mecha

import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicLong

internal data class RouterConnectionAttempt(
    val endpoint: String,
    val generation: Long,
)

internal class RouterConnectionCoordinator {
    private val generation = AtomicLong()
    private val activeAttempts = ConcurrentHashMap<String, RouterConnectionAttempt>()

    fun begin(endpoint: String): RouterConnectionAttempt = RouterConnectionAttempt(endpoint, generation.incrementAndGet()).also {
        activeAttempts[endpoint] = it
    }

    fun ensure(endpoint: String): RouterConnectionAttempt = activeAttempts[endpoint] ?: begin(endpoint)

    fun remove(endpoint: String) {
        activeAttempts.remove(endpoint)
    }

    fun isCurrent(attempt: RouterConnectionAttempt): Boolean = activeAttempts[attempt.endpoint] == attempt
}
