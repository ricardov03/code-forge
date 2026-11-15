package humanask

import "syscall"

const getTermios = syscall.TIOCGETA
const setTermios = syscall.TIOCSETA

// pendingInput is FIONREAD, _IOR('f', 127, int) in <sys/filio.h>.
const pendingInput = 0x4004667f
