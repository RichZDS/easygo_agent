//go:build !linux

package workshop

import (
	"errors"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd) error {
	return errors.New("native workshop runner requires Linux process groups")
}
func killProcessGroup(cmd *exec.Cmd) {}
