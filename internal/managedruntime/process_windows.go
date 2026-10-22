//go:build windows

package managedruntime

import (
	"os"
	"os/exec"
)

func prepareChild(*exec.Cmd)                  {}
func signalChild(cmd *exec.Cmd, _ bool) error { return cmd.Process.Kill() }
func Exec(path string, args, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
