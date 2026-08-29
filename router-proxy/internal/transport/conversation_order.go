package transport

// lockConversation covers the synchronous store-write and broadcast boundary
// for one participant pair. Reference counting removes idle locks so this
// table cannot grow with the conversation history.
func (r *Router) lockConversation(from, to string) func() {
	key := conversationKey(from, to)
	r.conversationLocksMu.Lock()
	lock := r.conversationLocks[key]
	if lock == nil {
		lock = &conversationLock{}
		r.conversationLocks[key] = lock
	}
	lock.refs++
	r.conversationLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		r.conversationLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(r.conversationLocks, key)
		}
		r.conversationLocksMu.Unlock()
	}
}

func conversationKey(from, to string) string {
	if to < from {
		from, to = to, from
	}
	return from + "\x00" + to
}
