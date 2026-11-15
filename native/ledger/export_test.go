package ledger

import "context"

// SetBeforeTornTailRecovery installs a hook that runs while AppendObject holds
// the append lock, just before torn-tail recovery. The returned func restores it.
func SetBeforeTornTailRecovery(hook func()) (restore func()) {
	previous := beforeTornTailRecovery
	beforeTornTailRecovery = hook
	return func() { beforeTornTailRecovery = previous }
}

// SetAppendPS points the lock-identity ps lookup at path and drops the cached
// identity; the returned func restores the real ps and drops the cache again.
func SetAppendPS(path string) (restore func()) {
	previous := appendPSPath
	appendSelf.Lock()
	appendPSPath, appendSelf.data = path, nil
	appendSelf.Unlock()
	return func() {
		appendSelf.Lock()
		appendPSPath, appendSelf.data = previous, nil
		appendSelf.Unlock()
	}
}

// SetAppendPSContext replaces the time limit of each lock-identity ps call;
// the returned func restores the real one.
func SetAppendPSContext(limit func() (context.Context, context.CancelFunc)) (restore func()) {
	previous := appendPSContext
	appendPSContext = limit
	return func() { appendPSContext = previous }
}

// AppendPSContext is the real time limit of one lock-identity ps call.
func AppendPSContext() (context.Context, context.CancelFunc) { return appendPSContext() }
