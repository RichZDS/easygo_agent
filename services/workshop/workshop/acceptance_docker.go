package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (r *DockerRunner) checkContainerOptions(name, workspace, checks string, check AcceptanceCheck) []string {
	args := r.containerOptions(name, workspace, "")
	for i, arg := range args {
		if arg == "--mount" && strings.Contains(args[i+1], ",dst=/workspace,") {
			args[i+1] += ",readonly"
		}
		if arg == "--entrypoint" {
			args[i+1] = check.Command[0]
		}
	}
	args = append(args, "--mount", "type=bind,src="+checks+",dst=/pack/checks,readonly,bind-propagation=rprivate", r.cfg.Image)
	return append(args, check.Command[1:]...)
}

// runCheck has no relay, capability environment, or writable task mount.
// A timed-out Docker client is followed by forced container cleanup, so the
// check and its descendants cannot continue after this method returns.
func (r *DockerRunner) runCheck(ctx context.Context, workspace, checks string, check AcceptanceCheck, output io.Writer) (exitCode int, timedOut bool, err error) {
	exitCode = -1
	r.mu.Lock()
	broken, ready := r.cleanupFailure, r.initialized
	r.mu.Unlock()
	if broken != nil || !ready {
		return -1, false, errors.New("check runner unavailable")
	}
	host, err := r.hostPath(workspace)
	if err != nil {
		return -1, false, errors.New("invalid check workspace")
	}
	packHost, err := r.hostPath(checks)
	if err != nil {
		return -1, false, errors.New("invalid check pack")
	}
	name := "easygo-check-" + uuid.NewString()
	defer func() {
		if cleanupErr := r.cleanup(name); cleanupErr != nil {
			err = errors.New("check container cleanup failed")
		}
	}()
	checkCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
	defer cancel()
	if _, err = r.output(checkCtx, r.checkContainerOptions(name, host, packHost, check)...); err != nil {
		return -1, checkCtx.Err() != nil && ctx.Err() == nil, errors.New("check container creation failed")
	}
	startErr := r.command(checkCtx, nil, output, output, "start", "--attach", name)
	if checkCtx.Err() != nil {
		if ctx.Err() != nil {
			return -1, false, ctx.Err()
		}
		return -1, true, nil
	}
	raw, err := r.output(checkCtx, "inspect", name)
	if err != nil {
		return -1, false, errors.New("check container inspection failed")
	}
	var objects []struct {
		State struct {
			Status   string
			Running  bool
			ExitCode int
			Error    string
		}
	}
	if json.Unmarshal([]byte(raw), &objects) != nil || len(objects) != 1 || objects[0].State.Running || objects[0].State.Status != "exited" || objects[0].State.Error != "" {
		return -1, false, errors.New("invalid check container completion")
	}
	exitCode = objects[0].State.ExitCode
	if startErr != nil && exitCode == 0 {
		return -1, false, errors.New("check attach failed")
	}
	if exitCode < 0 || exitCode > 255 {
		return -1, false, fmt.Errorf("invalid check exit code")
	}
	return exitCode, false, nil
}
