//go:build !unix

package cli

import "os/exec"

// detach is a no-op where there are no sessions to leave.
func detach(*exec.Cmd) {}
