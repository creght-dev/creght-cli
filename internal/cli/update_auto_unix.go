//go:build !windows

package cli

import "syscall"

// detachedSysProcAttr puts the worker in its own session, so it is not killed
// with the CLI's process group (a Ctrl-C in the user's terminal included) and
// keeps running after the CLI exits.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
