package host

import (
	"os/exec"
	"syscall"
)

func PrepareDetached(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
func prepareHelper(command *exec.Cmd) {}
