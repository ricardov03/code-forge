package main

import "syscall"

// dwpErrnoPlatform: the errnos libuv names on linux only (B535).
var dwpErrnoPlatform = []dwpErrnoEntry{{syscall.ENONET, "ENONET"}, {syscall.EREMOTEIO, "EREMOTEIO"}, {syscall.EUNATCH, "EUNATCH"}}
