//go:build !windows

package check

import "syscall"

const errRefused = syscall.ECONNREFUSED
