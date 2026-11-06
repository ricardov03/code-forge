package humanask

import "syscall"

const getTermios = syscall.TIOCGETA
const setTermios = syscall.TIOCSETA
