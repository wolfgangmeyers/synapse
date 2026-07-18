package life.meyers.mecha

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class RouterConnectionCoordinatorTest {
    @Test
    fun twoRoutersHaveIndependentCurrentConnections() {
        val coordinator = RouterConnectionCoordinator()
        val first = coordinator.begin("one.test:8765")
        val second = coordinator.begin("two.test:8765")

        assertTrue(coordinator.isCurrent(first))
        assertTrue(coordinator.isCurrent(second))
    }

    @Test
    fun reconnectingOneRouterDoesNotInvalidateTheOther() {
        val coordinator = RouterConnectionCoordinator()
        val first = coordinator.begin("one.test:8765")
        val second = coordinator.begin("two.test:8765")
        val reconnectedFirst = coordinator.begin("one.test:8765")

        assertFalse(coordinator.isCurrent(first))
        assertTrue(coordinator.isCurrent(reconnectedFirst))
        assertTrue(coordinator.isCurrent(second))
    }

    @Test
    fun reconciliationKeepsAHealthyRouterGeneration() {
        val coordinator = RouterConnectionCoordinator()
        val original = coordinator.ensure("one.test:8765")

        val afterActivityRecreation = coordinator.ensure("one.test:8765")

        assertTrue(coordinator.isCurrent(original))
        assertTrue(coordinator.isCurrent(afterActivityRecreation))
        assertTrue(original === afterActivityRecreation)
    }

    @Test
    fun removingOneRouterLeavesTheOtherRegistered() {
        val coordinator = RouterConnectionCoordinator()
        val first = coordinator.begin("one.test:8765")
        val second = coordinator.begin("two.test:8765")

        coordinator.remove("one.test:8765")

        assertFalse(coordinator.isCurrent(first))
        assertTrue(coordinator.isCurrent(second))
    }
}
