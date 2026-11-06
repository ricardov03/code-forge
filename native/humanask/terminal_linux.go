package humanask

import "syscall"

const getTermios = syscall.TCGETS
const setTermios = syscall.TCSETS
