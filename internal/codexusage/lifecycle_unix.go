//go:build !windows

package codexusage

import (
	"errors"
	"os/exec"
	"syscall"
)

type serverLifecycle struct {
	command *exec.Cmd
}

func newServerLifecycle(command *exec.Cmd) (*serverLifecycle, error) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	// The child becomes the leader of a new group before executing user code.
	command.SysProcAttr.Setpgid = true
	command.SysProcAttr.Pgid = 0
	command.SysProcAttr.Setsid = false
	return &serverLifecycle{command: command}, nil
}

func (lifecycle *serverLifecycle) started() error { return nil }

func (lifecycle *serverLifecycle) group() (int, error) {
	if lifecycle.command.Process == nil {
		return 0, errors.New("server process has not started")
	}
	group := lifecycle.command.Process.Pid
	if group <= 1 || group == syscall.Getpgrp() {
		return 0, errors.New("server process group is not isolated")
	}
	return group, nil
}

func (lifecycle *serverLifecycle) terminate() error {
	group, err := lifecycle.group()
	if err != nil {
		return err
	}
	err = syscall.Kill(-group, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (lifecycle *serverLifecycle) empty() (bool, error) {
	group, err := lifecycle.group()
	if err != nil {
		return false, err
	}
	err = syscall.Kill(-group, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}

func (lifecycle *serverLifecycle) close() error { return nil }
