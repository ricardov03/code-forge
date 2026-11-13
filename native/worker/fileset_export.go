package worker

// ComputeFileSet is computeFileSet for callers outside the worker (B612): the
// sorted tracked-changes-since-base union untracked files. ok false is Node's
// throw; err is ErrUnsupported for a name that is not UTF-8.
func ComputeFileSet(cwd, base string) ([]string, bool, error) { return computeFileSet(cwd, base) }
