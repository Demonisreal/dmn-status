package check

import "syscall"

// syscall.ECONNREFUSED ist unter windows ein eigener wert, winsock liefert WSAECONNREFUSED
const errRefused = syscall.Errno(10061)
