//go:build windows

package cli

import "syscall"

// detachedSysProcAttr severs the worker from this console, so it keeps running
// after the CLI exits and never pops a window of its own.
func detachedSysProcAttr() *syscall.SysProcAttr {
	const detachedProcess = 0x00000008
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
		HideWindow:    true,
	}
}
