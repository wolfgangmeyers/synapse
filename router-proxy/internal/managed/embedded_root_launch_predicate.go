package managed

// embeddedShouldAppendWorkerLaunch changes only the first root declaration in
// the disposable tagged build. Workers and subsequent root turns retain it.
func embeddedShouldAppendWorkerLaunch(role string, ownerToken int64, pendingInitial, initialTurn bool) bool {
	return role == "worker" || (role == "root" && !(embeddedRootLaunchAblation && ownerToken == 1 && pendingInitial && initialTurn))
}
