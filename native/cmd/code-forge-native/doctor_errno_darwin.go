package main

import "syscall"

// dwpErrnoPlatform: the errnos libuv names on darwin only (B535).
var dwpErrnoPlatform = []dwpErrnoEntry{{syscall.EFTYPE, "EFTYPE"}}
