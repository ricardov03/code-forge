package ledger

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
