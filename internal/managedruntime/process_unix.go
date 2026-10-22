//go:build !windows

package managedruntime

import (
	"os/exec"
	"syscall"
)

func prepareChild(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func signalChild(cmd *exec.Cmd, kill bool) error {
	signal := syscall.SIGTERM
	if kill {
		signal = syscall.SIGKILL
	}
	return syscall.Kill(-cmd.Process.Pid, signal)
}

func Exec(path string, args, env []string) error {
	return syscall.Exec(path, append([]string{path}, args...), env)
}
