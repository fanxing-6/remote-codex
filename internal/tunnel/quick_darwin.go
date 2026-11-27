package tunnel

import "os/exec"

func prepareQuick(command *exec.Cmd) {}

func trackQuick(command *exec.Cmd) (func() error, error) { return func() error { return nil }, nil }
