package host

import (
	"os/exec"
	"syscall"
)

func PrepareDetached(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000200 | 0x00000008}
	return nil
}

func prepareHelper(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
