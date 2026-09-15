//go:build race

package transport

// The race detector's shadow-memory instrumentation changes allocation totals.
const allocationBudgetEnabled = false
