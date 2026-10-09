//go:build unix

package launcher

import (
	"os/exec"
	"syscall"
)

func detachAttrs(cmd *exec.Cmd) {
	// New session: the tool survives dockgit exiting or the terminal closing.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
