//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach puts the daemon in its own session so it outlives the command
// that spawned it and never receives that command's Ctrl-C.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
