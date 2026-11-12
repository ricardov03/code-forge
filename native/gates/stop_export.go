package gates

// StopRemaining stops what is left of a process group after its leader
// exited (TERM, then KILL after the grace), as Node's stopMembers.
func StopRemaining(pgid int) { stopRemaining(pgid) }
