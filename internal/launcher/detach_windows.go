package launcher

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func detachAttrs(cmd *exec.Cmd) {
	// New process group so Ctrl+C in dockgit doesn't reach it, and no console
	// window for .cmd shims like code.cmd.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
}
