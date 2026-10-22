//go:build windows

package managedruntime

import "os/exec"

func prepareChild(*exec.Cmd)                  {}
func signalChild(cmd *exec.Cmd, _ bool) error { return cmd.Process.Kill() }
func Exec(path string, args, env []string) error {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	return cmd.Run()
}
